package http

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"any2api-go/internal/core"
	"any2api-go/internal/platforms"
)

func testAppConfig() core.AppConfig {
	cfg := core.LoadAppConfigFromEnv()
	cfg.APIKey = "test-key"
	cfg.DefaultProvider = "cursor"
	return cfg
}

func TestModelsEndpointIncludesFourProviders(t *testing.T) {
	cfg := testAppConfig()
	h := NewHandler(platforms.DefaultRegistry(cfg), cfg)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	var body struct {
		Object string                   `json:"object"`
		Data   []map[string]interface{} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Object != "list" {
		t.Fatalf("expected object=list, got %q", body.Object)
	}
	if len(body.Data) < 4 {
		t.Fatalf("expected at least 4 models, got %d", len(body.Data))
	}
}

func TestOpenAIChatUsesRequestedProvider(t *testing.T) {
	cfg := testAppConfig()
	h := NewHandler(platforms.DefaultRegistry(cfg), cfg)
	payload := []byte(`{"provider":"kiro","model":"claude-sonnet-4.6","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer test-key")
	h.Routes().ServeHTTP(rec, req)
	if rec.Header().Get("X-Newplatform2API-Provider") != "kiro" {
		t.Fatalf("expected provider header to be kiro, got %q", rec.Header().Get("X-Newplatform2API-Provider"))
	}
}

func TestOpenAIChatRejectsProviderWithoutOpenAICompatibility(t *testing.T) {
	cfg := testAppConfig()
	registry := core.NewRegistry("anthropic-only")
	registry.Register(testAnthropicOnlyProvider{})
	h := NewHandler(registry, cfg)
	payload := []byte(`{"provider":"anthropic-only","model":"stub-model","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer test-key")
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if body["error"] != `provider "anthropic-only" does not support OpenAI compatible endpoint` {
		t.Fatalf("unexpected error body: %#v", body)
	}
}

func TestOpenAIChatOrchidsUsesConfiguredUpstream(t *testing.T) {
	clerk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/client":
			cookie, err := r.Cookie("__client")
			if err != nil || cookie.Value != "orchids-client" {
				t.Fatalf("expected orchids client cookie on clerk bootstrap, got %v %v", cookie, err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"response":{"last_active_session_id":"sess_test","sessions":[{"user":{"id":"user_test","email_addresses":[{"email_address":"orchids@example.com"}]}}]}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/client/sessions/sess_test/tokens":
			if got := r.Header.Get("Cookie"); !strings.Contains(got, "__client=orchids-client") || !strings.Contains(got, "__client_uat=") {
				t.Fatalf("unexpected orchids token cookie header: %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jwt":"orchids-jwt"}`))
		default:
			t.Fatalf("unexpected orchids clerk request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer clerk.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer orchids-jwt" {
			t.Fatalf("expected orchids bearer token, got %q", got)
		}
		var body struct {
			Prompt    string `json:"prompt"`
			ProjectID string `json:"projectId"`
			AgentMode string `json:"agentMode"`
			Email     string `json:"email"`
			UserID    string `json:"userId"`
			Model     string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode orchids upstream request: %v", err)
		}
		if body.Model != "claude-sonnet-4-5" {
			t.Fatalf("unexpected orchids model: %q", body.Model)
		}
		if body.ProjectID != "project-test" || body.AgentMode != "claude-opus-4.5" || body.Email != "orchids@example.com" || body.UserID != "user_test" {
			t.Fatalf("unexpected orchids payload identity: %#v", body)
		}
		if !strings.Contains(body.Prompt, "<user_request>\nhi\n</user_request>") {
			t.Fatalf("unexpected orchids prompt: %s", body.Prompt)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"model\",\"event\":{\"type\":\"text-start\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"model\",\"event\":{\"type\":\"text-delta\",\"delta\":\"hello \"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"model\",\"event\":{\"type\":\"text-delta\",\"delta\":\"orchids\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"model\",\"event\":{\"type\":\"finish\",\"finishReason\":\"stop\"}}\n\n"))
	}))
	defer upstream.Close()

	t.Setenv("NEWPLATFORM2API_ORCHIDS_API_URL", upstream.URL)
	t.Setenv("NEWPLATFORM2API_ORCHIDS_CLERK_URL", clerk.URL)
	t.Setenv("NEWPLATFORM2API_ORCHIDS_CLIENT_COOKIE", "orchids-client")
	t.Setenv("NEWPLATFORM2API_ORCHIDS_PROJECT_ID", "project-test")
	t.Setenv("NEWPLATFORM2API_ORCHIDS_AGENT_MODE", "claude-opus-4.5")

	cfg := testAppConfig()
	h := NewHandler(platforms.DefaultRegistry(cfg), cfg)
	payload := []byte(`{"provider":"orchids","model":"claude-sonnet-4.5","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer test-key")
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Newplatform2API-Provider") != "orchids" {
		t.Fatalf("expected provider header to be orchids, got %q", rec.Header().Get("X-Newplatform2API-Provider"))
	}
	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode orchids openai response: %v", err)
	}
	if len(response.Choices) != 1 || response.Choices[0].Message.Content != "hello orchids" {
		t.Fatalf("unexpected orchids response: %#v", response.Choices)
	}
}

func TestOpenAIChatOrchidsAcceptsRequestCredentialHeader(t *testing.T) {
	clerk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/client":
			cookie, err := r.Cookie("__client")
			if err != nil || cookie.Value != "orchids-header-client" {
				t.Fatalf("expected orchids client cookie on clerk bootstrap, got %v %v", cookie, err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"response":{"last_active_session_id":"sess_header","sessions":[{"user":{"id":"user_header","email_addresses":[{"email_address":"header@example.com"}]}}]}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/client/sessions/sess_header/tokens":
			if got := r.Header.Get("Cookie"); !strings.Contains(got, "__client=orchids-header-client") {
				t.Fatalf("unexpected orchids token cookie header: %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jwt":"orchids-jwt-header"}`))
		default:
			t.Fatalf("unexpected orchids clerk request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer clerk.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer orchids-jwt-header" {
			t.Fatalf("expected orchids bearer token, got %q", got)
		}
		var body struct {
			ProjectID string `json:"projectId"`
			Email     string `json:"email"`
			UserID    string `json:"userId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode orchids upstream request: %v", err)
		}
		if body.ProjectID != "project-from-header-test" || body.Email != "header@example.com" || body.UserID != "user_header" {
			t.Fatalf("unexpected orchids payload identity: %#v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"model\",\"event\":{\"type\":\"text-delta\",\"delta\":\"hello request header\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"model\",\"event\":{\"type\":\"finish\",\"finishReason\":\"stop\"}}\n\n"))
	}))
	defer upstream.Close()

	t.Setenv("NEWPLATFORM2API_ORCHIDS_API_URL", upstream.URL)
	t.Setenv("NEWPLATFORM2API_ORCHIDS_CLERK_URL", clerk.URL)
	t.Setenv("NEWPLATFORM2API_ORCHIDS_PROJECT_ID", "project-from-header-test")

	cfg := testAppConfig()
	h := NewHandler(platforms.DefaultRegistry(cfg), cfg)
	payload := []byte(`{"provider":"orchids","model":"claude-sonnet-4.5","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("X-Orchids-Client-Cookie", "orchids-header-client")
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode orchids openai response: %v", err)
	}
	if len(response.Choices) != 1 || response.Choices[0].Message.Content != "hello request header" {
		t.Fatalf("unexpected orchids response: %#v", response.Choices)
	}
}

func TestAnthropicMessagesRejectsProviderWithoutAnthropicCompatibility(t *testing.T) {
	cfg := testAppConfig()
	h := NewHandler(platforms.DefaultRegistry(cfg), cfg)
	payload := []byte(`{"provider":"grok","model":"grok-4","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer test-key")
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if body["error"] != `provider "grok" does not support Anthropic compatible endpoint` {
		t.Fatalf("unexpected error body: %#v", body)
	}
}

func TestProtectedRoutesRequireAPIKey(t *testing.T) {
	cfg := testAppConfig()
	h := NewHandler(platforms.DefaultRegistry(cfg), cfg)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without api key, got %d", rec.Code)
	}
}

func TestOpenAIChatCursorUsesConfiguredUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if got := r.Header.Get("Cookie"); !strings.Contains(got, "WorkosCursorSessionToken=test-cookie") {
			t.Errorf("expected cursor cookie header, got %q", got)
		}
		if got := r.Header.Get("X-Is-Human"); got != "test-human-token" {
			t.Errorf("expected x-is-human header, got %q", got)
		}

		var body struct {
			Model    string `json:"model"`
			Trigger  string `json:"trigger"`
			Messages []struct {
				Role  string `json:"role"`
				Parts []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		if body.Model != "anthropic/claude-sonnet-4.6" {
			t.Errorf("expected mapped upstream model, got %q", body.Model)
		}
		if body.Trigger != "submit-message" {
			t.Errorf("expected trigger submit-message, got %q", body.Trigger)
		}
		if len(body.Messages) != 1 || len(body.Messages[0].Parts) != 1 || body.Messages[0].Parts[0].Text != "hi" {
			t.Errorf("unexpected upstream messages: %#v", body.Messages)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"delta\",\"delta\":\"hello \"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"delta\",\"delta\":\"world\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"finish\"}\n\n"))
	}))
	defer upstream.Close()

	t.Setenv("NEWPLATFORM2API_CURSOR_API_URL", upstream.URL)
	t.Setenv("NEWPLATFORM2API_CURSOR_COOKIE", "WorkosCursorSessionToken=test-cookie")
	t.Setenv("NEWPLATFORM2API_CURSOR_X_IS_HUMAN", "test-human-token")

	cfg := testAppConfig()
	h := NewHandler(platforms.DefaultRegistry(cfg), cfg)
	payload := []byte(`{"provider":"cursor","model":"claude-sonnet-4.6","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer test-key")
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Newplatform2API-Provider") != "cursor" {
		t.Fatalf("expected provider header to be cursor, got %q", rec.Header().Get("X-Newplatform2API-Provider"))
	}

	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Choices) != 1 || response.Choices[0].Message.Content != "hello world" {
		t.Fatalf("unexpected assistant content: %#v", response.Choices)
	}
}

func TestOpenAIChatCursorStreamsConfiguredUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"delta\",\"delta\":\"hello\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"delta\",\"delta\":\" framework\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"finish\"}\n\n"))
	}))
	defer upstream.Close()

	t.Setenv("NEWPLATFORM2API_CURSOR_API_URL", upstream.URL)
	t.Setenv("NEWPLATFORM2API_CURSOR_COOKIE", "WorkosCursorSessionToken=test-cookie")
	t.Setenv("NEWPLATFORM2API_CURSOR_X_IS_HUMAN", "test-human-token")

	cfg := testAppConfig()
	h := NewHandler(platforms.DefaultRegistry(cfg), cfg)
	payload := []byte(`{"provider":"cursor","model":"claude-sonnet-4.6","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer test-key")
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"content":"hello"`) || !strings.Contains(body, `"content":" framework"`) {
		t.Fatalf("expected streamed deltas in response body, got %s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("expected [DONE] marker, got %s", body)
	}
}

func TestAnthropicMessagesCursorUsesConfiguredUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Role  string `json:"role"`
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		if len(body.Messages) != 2 || body.Messages[0].Role != "system" || body.Messages[0].Parts[0].Text != "be precise" {
			t.Fatalf("expected system message to be prepended, got %#v", body.Messages)
		}
		if body.Messages[1].Role != "user" || body.Messages[1].Parts[0].Text != "hi" {
			t.Fatalf("unexpected user message: %#v", body.Messages)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"delta\",\"delta\":\"hello anthropic\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"finish\"}\n\n"))
	}))
	defer upstream.Close()

	t.Setenv("NEWPLATFORM2API_CURSOR_API_URL", upstream.URL)
	t.Setenv("NEWPLATFORM2API_CURSOR_COOKIE", "WorkosCursorSessionToken=test-cookie")
	t.Setenv("NEWPLATFORM2API_CURSOR_X_IS_HUMAN", "test-human-token")

	cfg := testAppConfig()
	h := NewHandler(platforms.DefaultRegistry(cfg), cfg)
	payload := []byte(`{"provider":"cursor","model":"claude-sonnet-4.6","system":"be precise","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer test-key")
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Content) != 1 || response.Content[0].Text != "hello anthropic" {
		t.Fatalf("unexpected anthropic response: %#v", response.Content)
	}
}

func TestAnthropicMessagesOrchidsUsesConfiguredUpstream(t *testing.T) {
	clerk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/client":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"response":{"last_active_session_id":"sess_test","sessions":[{"user":{"id":"user_test","email_addresses":[{"email_address":"orchids@example.com"}]}}]}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/client/sessions/sess_test/tokens":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jwt":"orchids-jwt"}`))
		default:
			t.Fatalf("unexpected orchids clerk request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer clerk.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Prompt string `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode orchids upstream request: %v", err)
		}
		if !strings.Contains(body.Prompt, "<client_system>\nbe precise\n</client_system>") {
			t.Fatalf("expected orchids prompt to include system block, got %s", body.Prompt)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"model\",\"event\":{\"type\":\"text-delta\",\"delta\":\"hello orchids anthropic\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"model\",\"event\":{\"type\":\"finish\",\"finishReason\":\"stop\"}}\n\n"))
	}))
	defer upstream.Close()

	t.Setenv("NEWPLATFORM2API_ORCHIDS_API_URL", upstream.URL)
	t.Setenv("NEWPLATFORM2API_ORCHIDS_CLERK_URL", clerk.URL)
	t.Setenv("NEWPLATFORM2API_ORCHIDS_CLIENT_COOKIE", "orchids-client")
	t.Setenv("NEWPLATFORM2API_ORCHIDS_PROJECT_ID", "project-test")

	cfg := testAppConfig()
	h := NewHandler(platforms.DefaultRegistry(cfg), cfg)
	payload := []byte(`{"provider":"orchids","model":"claude-sonnet-4.5","system":"be precise","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer test-key")
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Newplatform2API-Provider") != "orchids" {
		t.Fatalf("expected provider header to be orchids, got %q", rec.Header().Get("X-Newplatform2API-Provider"))
	}
	var response struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Content) != 1 || response.Content[0].Text != "hello orchids anthropic" {
		t.Fatalf("unexpected orchids response: %#v", response.Content)
	}
}

func TestAnthropicMessagesCursorStreamsConfiguredUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"delta\",\"delta\":\"hello\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"delta\",\"delta\":\" anthropic\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"finish\"}\n\n"))
	}))
	defer upstream.Close()

	t.Setenv("NEWPLATFORM2API_CURSOR_API_URL", upstream.URL)
	t.Setenv("NEWPLATFORM2API_CURSOR_COOKIE", "WorkosCursorSessionToken=test-cookie")
	t.Setenv("NEWPLATFORM2API_CURSOR_X_IS_HUMAN", "test-human-token")

	cfg := testAppConfig()
	h := NewHandler(platforms.DefaultRegistry(cfg), cfg)
	payload := []byte(`{"provider":"cursor","model":"claude-sonnet-4.6","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer test-key")
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: message_start") || !strings.Contains(body, "event: content_block_delta") || !strings.Contains(body, `"text":"hello"`) || !strings.Contains(body, `"text":" anthropic"`) || !strings.Contains(body, "event: message_stop") {
		t.Fatalf("unexpected anthropic stream body: %s", body)
	}
}

func TestOpenAIChatKiroUsesConfiguredUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer kiro-token" {
			t.Fatalf("expected kiro bearer token, got %q", got)
		}
		if got := r.Header.Get("X-Amz-Target"); got != "AmazonCodeWhispererStreamingService.GenerateAssistantResponse" {
			t.Fatalf("unexpected x-amz-target: %q", got)
		}
		var body struct {
			ConversationState struct {
				CurrentMessage struct {
					UserInputMessage struct {
						Content string `json:"content"`
						ModelID string `json:"modelId"`
					} `json:"userInputMessage"`
				} `json:"currentMessage"`
			} `json:"conversationState"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		if body.ConversationState.CurrentMessage.UserInputMessage.Content != "hi" {
			t.Fatalf("unexpected kiro current content: %q", body.ConversationState.CurrentMessage.UserInputMessage.Content)
		}
		if body.ConversationState.CurrentMessage.UserInputMessage.ModelID != "claude-sonnet-4.6" {
			t.Fatalf("unexpected kiro model id: %q", body.ConversationState.CurrentMessage.UserInputMessage.ModelID)
		}
		_, _ = w.Write(encodeKiroTestStream(kiroTestEvent{eventType: "assistantResponseEvent", payload: map[string]interface{}{"content": "hello kiro"}}))
	}))
	defer upstream.Close()

	t.Setenv("NEWPLATFORM2API_KIRO_ACCESS_TOKEN", "kiro-token")
	t.Setenv("NEWPLATFORM2API_KIRO_CODEWHISPERER_URL", upstream.URL)
	t.Setenv("NEWPLATFORM2API_KIRO_AMAZONQ_URL", upstream.URL)

	cfg := testAppConfig()
	h := NewHandler(platforms.DefaultRegistry(cfg), cfg)
	payload := []byte(`{"provider":"kiro","model":"claude-sonnet-4.6","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer test-key")
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Choices) != 1 || response.Choices[0].Message.Content != "hello kiro" {
		t.Fatalf("unexpected kiro response: %#v", response.Choices)
	}
}

func TestAnthropicMessagesKiroStreamsConfiguredUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ConversationState struct {
				CurrentMessage struct {
					UserInputMessage struct {
						Content string `json:"content"`
					} `json:"userInputMessage"`
				} `json:"currentMessage"`
			} `json:"conversationState"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		if body.ConversationState.CurrentMessage.UserInputMessage.Content != "be precise\n\nhi" {
			t.Fatalf("unexpected anthropic kiro content: %q", body.ConversationState.CurrentMessage.UserInputMessage.Content)
		}
		_, _ = w.Write(encodeKiroTestStream(
			kiroTestEvent{eventType: "assistantResponseEvent", payload: map[string]interface{}{"content": "hello"}},
			kiroTestEvent{eventType: "assistantResponseEvent", payload: map[string]interface{}{"content": "hello anthropic"}},
		))
	}))
	defer upstream.Close()

	t.Setenv("NEWPLATFORM2API_KIRO_ACCESS_TOKEN", "kiro-token")
	t.Setenv("NEWPLATFORM2API_KIRO_CODEWHISPERER_URL", upstream.URL)
	t.Setenv("NEWPLATFORM2API_KIRO_AMAZONQ_URL", upstream.URL)

	cfg := testAppConfig()
	h := NewHandler(platforms.DefaultRegistry(cfg), cfg)
	payload := []byte(`{"provider":"kiro","model":"claude-sonnet-4.6","system":"be precise","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer test-key")
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"text":"hello"`) || !strings.Contains(body, `"text":" anthropic"`) {
		t.Fatalf("unexpected kiro anthropic stream body: %s", body)
	}
}

func TestOpenAIChatGrokUsesConfiguredUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Cookie"); !strings.Contains(got, "sso=grok-cookie") {
			t.Fatalf("expected grok sso cookie, got %q", got)
		}
		var body struct {
			Message   string `json:"message"`
			ModelName string `json:"modelName"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		if body.Message != "assistant: earlier\n\nhi" {
			t.Fatalf("unexpected flattened grok message: %q", body.Message)
		}
		if body.ModelName != "grok-4" {
			t.Fatalf("unexpected grok model: %q", body.ModelName)
		}
		_, _ = w.Write([]byte("{\"result\":{\"response\":{\"token\":\"hello\"}}}\n"))
		_, _ = w.Write([]byte("{\"result\":{\"response\":{\"token\":\" grok\"}}}\n"))
	}))
	defer upstream.Close()

	t.Setenv("NEWPLATFORM2API_GROK_API_URL", upstream.URL)
	t.Setenv("NEWPLATFORM2API_GROK_COOKIE_TOKEN", "grok-cookie")

	cfg := testAppConfig()
	h := NewHandler(platforms.DefaultRegistry(cfg), cfg)
	payload := []byte(`{"provider":"grok","model":"grok-4","messages":[{"role":"assistant","content":"earlier"},{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer test-key")
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Choices) != 1 || response.Choices[0].Message.Content != "hello grok" {
		t.Fatalf("unexpected grok response: %#v", response.Choices)
	}
}

func TestOpenAIChatGrokStreamsConfiguredUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{\"result\":{\"response\":{\"token\":\"hello\"}}}\n"))
		_, _ = w.Write([]byte("{\"result\":{\"response\":{\"token\":\" world\"}}}\n"))
	}))
	defer upstream.Close()

	t.Setenv("NEWPLATFORM2API_GROK_API_URL", upstream.URL)
	t.Setenv("NEWPLATFORM2API_GROK_COOKIE_TOKEN", "grok-cookie")

	cfg := testAppConfig()
	h := NewHandler(platforms.DefaultRegistry(cfg), cfg)
	payload := []byte(`{"provider":"grok","model":"grok-4","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer test-key")
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"content":"hello"`) || !strings.Contains(body, `"content":" world"`) || !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("unexpected grok stream body: %s", body)
	}
}

type kiroTestEvent struct {
	eventType string
	payload   interface{}
}

func encodeKiroTestStream(events ...kiroTestEvent) []byte {
	var output bytes.Buffer
	for _, event := range events {
		payload, _ := json.Marshal(event.payload)
		headers := encodeKiroTestHeader(":event-type", event.eventType)
		prelude := make([]byte, 12)
		totalLength := uint32(12 + len(headers) + len(payload) + 4)
		binary.BigEndian.PutUint32(prelude[0:4], totalLength)
		binary.BigEndian.PutUint32(prelude[4:8], uint32(len(headers)))
		output.Write(prelude)
		output.Write(headers)
		output.Write(payload)
		output.Write([]byte{0, 0, 0, 0})
	}
	return output.Bytes()
}

func encodeKiroTestHeader(name string, value string) []byte {
	header := make([]byte, 0, 1+len(name)+1+2+len(value))
	header = append(header, byte(len(name)))
	header = append(header, []byte(name)...)
	header = append(header, 7)
	header = append(header, byte(len(value)>>8), byte(len(value)))
	header = append(header, []byte(value)...)
	return header
}

func TestAdminSettingsCanRotateRuntimeAPIKey(t *testing.T) {
	h, cookie := newRuntimeAdminHandler(t)

	settingsPayload := []byte(`{"apiKey":"runtime-key","defaultProvider":"kiro","adminPassword":"new-secret"}`)
	settingsReq := httptest.NewRequest(http.MethodPut, "/admin/api/settings", bytes.NewReader(settingsPayload))
	settingsReq.AddCookie(cookie)
	settingsRec := httptest.NewRecorder()
	h.Routes().ServeHTTP(settingsRec, settingsReq)
	if settingsRec.Code != http.StatusOK {
		t.Fatalf("unexpected settings status: %d body=%s", settingsRec.Code, settingsRec.Body.String())
	}

	unauthorized := httptest.NewRecorder()
	unauthorizedReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	unauthorizedReq.Header.Set("Authorization", "Bearer test-key")
	h.Routes().ServeHTTP(unauthorized, unauthorizedReq)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("expected old api key to fail, got %d", unauthorized.Code)
	}

	authorized := httptest.NewRecorder()
	authorizedReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	authorizedReq.Header.Set("Authorization", "Bearer runtime-key")
	h.Routes().ServeHTTP(authorized, authorizedReq)
	if authorized.Code != http.StatusOK {
		t.Fatalf("expected new api key to work, got %d body=%s", authorized.Code, authorized.Body.String())
	}
}

func TestAdminProviderEndpointsPersistCountsAndSelections(t *testing.T) {
	h, cookie := newRuntimeAdminHandler(t)

	cursorPayload := []byte(`{"config":{"cookie":"cursor-cookie","scriptUrl":"https://cursor.com/_next/static/chunks/pages/_app.js","userAgent":"Mozilla/5.0","referer":"https://cursor.com/"}}`)
	cursorReq := httptest.NewRequest(http.MethodPut, "/admin/api/providers/cursor/config", bytes.NewReader(cursorPayload))
	cursorReq.AddCookie(cookie)
	cursorRec := httptest.NewRecorder()
	h.Routes().ServeHTTP(cursorRec, cursorReq)
	if cursorRec.Code != http.StatusOK {
		t.Fatalf("unexpected cursor status: %d body=%s", cursorRec.Code, cursorRec.Body.String())
	}

	kiroPayload := []byte(`{"accounts":[{"name":"Primary","accessToken":"kiro-token","machineId":"machine-1","preferredEndpoint":"amazonq","active":true}]}`)
	kiroReq := httptest.NewRequest(http.MethodPut, "/admin/api/providers/kiro/accounts", bytes.NewReader(kiroPayload))
	kiroReq.AddCookie(cookie)
	kiroRec := httptest.NewRecorder()
	h.Routes().ServeHTTP(kiroRec, kiroReq)
	if kiroRec.Code != http.StatusOK {
		t.Fatalf("unexpected kiro status: %d body=%s", kiroRec.Code, kiroRec.Body.String())
	}

	grokPayload := []byte(`{"tokens":[{"name":"Main","cookieToken":"grok-cookie","active":true}]}`)
	grokReq := httptest.NewRequest(http.MethodPut, "/admin/api/providers/grok/tokens", bytes.NewReader(grokPayload))
	grokReq.AddCookie(cookie)
	grokRec := httptest.NewRecorder()
	h.Routes().ServeHTTP(grokRec, grokReq)
	if grokRec.Code != http.StatusOK {
		t.Fatalf("unexpected grok status: %d body=%s", grokRec.Code, grokRec.Body.String())
	}

	orchidsPayload := []byte(`{"config":{"clientCookie":"orchids-cookie","clientUat":"123","sessionId":"sess-1","projectId":"project-1","userId":"user-1","email":"user@example.com","agentMode":"claude-opus-4.5"}}`)
	orchidsReq := httptest.NewRequest(http.MethodPut, "/admin/api/providers/orchids/config", bytes.NewReader(orchidsPayload))
	orchidsReq.AddCookie(cookie)
	orchidsRec := httptest.NewRecorder()
	h.Routes().ServeHTTP(orchidsRec, orchidsReq)
	if orchidsRec.Code != http.StatusOK {
		t.Fatalf("unexpected orchids status: %d body=%s", orchidsRec.Code, orchidsRec.Body.String())
	}

	statusReq := httptest.NewRequest(http.MethodGet, "/admin/api/status", nil)
	statusReq.AddCookie(cookie)
	statusRec := httptest.NewRecorder()
	h.Routes().ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("unexpected status endpoint status: %d body=%s", statusRec.Code, statusRec.Body.String())
	}
	var body struct {
		Providers struct {
			Cursor struct {
				Count      int    `json:"count"`
				Configured bool   `json:"configured"`
				Active     string `json:"active"`
			} `json:"cursor"`
			Kiro struct {
				Count      int    `json:"count"`
				Configured bool   `json:"configured"`
				Active     string `json:"active"`
			} `json:"kiro"`
			Grok struct {
				Count      int    `json:"count"`
				Configured bool   `json:"configured"`
				Active     string `json:"active"`
			} `json:"grok"`
			Orchids struct {
				Count      int    `json:"count"`
				Configured bool   `json:"configured"`
				Active     string `json:"active"`
			} `json:"orchids"`
		} `json:"providers"`
	}
	if err := json.NewDecoder(statusRec.Body).Decode(&body); err != nil {
		t.Fatalf("decode admin status: %v", err)
	}
	if body.Providers.Cursor.Count != 1 || !body.Providers.Cursor.Configured || body.Providers.Cursor.Active == "" {
		t.Fatalf("unexpected cursor status: %#v", body.Providers.Cursor)
	}
	if body.Providers.Kiro.Count != 1 || !body.Providers.Kiro.Configured || body.Providers.Kiro.Active == "" {
		t.Fatalf("unexpected kiro status: %#v", body.Providers.Kiro)
	}
	if body.Providers.Grok.Count != 1 || !body.Providers.Grok.Configured || body.Providers.Grok.Active == "" {
		t.Fatalf("unexpected grok status: %#v", body.Providers.Grok)
	}
	if body.Providers.Orchids.Count != 1 || !body.Providers.Orchids.Configured || body.Providers.Orchids.Active == "" {
		t.Fatalf("unexpected orchids status: %#v", body.Providers.Orchids)
	}
}

func TestAdminSharedContractMetaEndpoint(t *testing.T) {
	h := newRuntimeHandler(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/meta", nil)
	req.Header.Set("Origin", "http://tauri.localhost")
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected meta status: %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "http://tauri.localhost" {
		t.Fatalf("expected cors origin to be echoed, got %q", rec.Header().Get("Access-Control-Allow-Origin"))
	}
	var body struct {
		Backend struct {
			Language string `json:"language"`
			Version  string `json:"version"`
		} `json:"backend"`
		Auth struct {
			Mode string `json:"mode"`
		} `json:"auth"`
		Features struct {
			Providers          bool `json:"providers"`
			Credentials        bool `json:"credentials"`
			ProviderState      bool `json:"providerState"`
			Stats              bool `json:"stats"`
			Logs               bool `json:"logs"`
			Users              bool `json:"users"`
			ConfigImportExport bool `json:"configImportExport"`
		} `json:"features"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode meta response: %v", err)
	}
	if body.Backend.Language != "go" {
		t.Fatalf("expected backend language go, got %q", body.Backend.Language)
	}
	if strings.TrimSpace(body.Backend.Version) == "" {
		t.Fatal("expected backend version to be set")
	}
	if body.Auth.Mode != "session_cookie" {
		t.Fatalf("expected auth mode session_cookie, got %q", body.Auth.Mode)
	}
	if !body.Features.Providers || !body.Features.Credentials || !body.Features.ProviderState {
		t.Fatalf("expected core admin features enabled, got %#v", body.Features)
	}
	if body.Features.Stats || body.Features.Logs || body.Features.Users || body.Features.ConfigImportExport {
		t.Fatalf("expected optional features disabled by default, got %#v", body.Features)
	}
}

func TestAdminSharedContractSessionLifecycle(t *testing.T) {
	h := newRuntimeHandler(t)
	unauthorizedRec := httptest.NewRecorder()
	unauthorizedReq := httptest.NewRequest(http.MethodGet, "/api/admin/auth/session", nil)
	h.Routes().ServeHTTP(unauthorizedRec, unauthorizedReq)
	if unauthorizedRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated session check to return 401, got %d body=%s", unauthorizedRec.Code, unauthorizedRec.Body.String())
	}

	loginPayload := []byte(`{"password":"admin-secret"}`)
	loginReq := httptest.NewRequest(http.MethodPost, "/api/admin/auth/login", bytes.NewReader(loginPayload))
	loginRec := httptest.NewRecorder()
	h.Routes().ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("unexpected contract login status: %d body=%s", loginRec.Code, loginRec.Body.String())
	}
	var loginBody struct {
		OK    bool   `json:"ok"`
		Token string `json:"token"`
	}
	if err := json.NewDecoder(bytes.NewReader(loginRec.Body.Bytes())).Decode(&loginBody); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if !loginBody.OK || strings.TrimSpace(loginBody.Token) == "" {
		t.Fatalf("expected login token in response, got %#v", loginBody)
	}
	var sessionCookie *http.Cookie
	for _, cookie := range loginRec.Result().Cookies() {
		if cookie.Name == adminSessionCookieName {
			sessionCookie = cookie
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("missing session cookie after contract login")
	}

	sessionRec := httptest.NewRecorder()
	sessionReq := httptest.NewRequest(http.MethodGet, "/api/admin/auth/session", nil)
	sessionReq.Header.Set("Authorization", "Bearer "+loginBody.Token)
	h.Routes().ServeHTTP(sessionRec, sessionReq)
	if sessionRec.Code != http.StatusOK {
		t.Fatalf("unexpected contract session status: %d body=%s", sessionRec.Code, sessionRec.Body.String())
	}
	var sessionBody struct {
		Authenticated bool   `json:"authenticated"`
		ExpiresAt     string `json:"expiresAt"`
		User          struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Role string `json:"role"`
		} `json:"user"`
	}
	if err := json.NewDecoder(sessionRec.Body).Decode(&sessionBody); err != nil {
		t.Fatalf("decode session response: %v", err)
	}
	if !sessionBody.Authenticated {
		t.Fatal("expected authenticated session")
	}
	if sessionBody.User.Role != "admin" || sessionBody.User.ID == "" || sessionBody.User.Name == "" {
		t.Fatalf("unexpected session user: %#v", sessionBody.User)
	}
	if strings.TrimSpace(sessionBody.ExpiresAt) == "" {
		t.Fatal("expected session expiry to be present")
	}
	if sessionCookie == nil {
		t.Fatal("expected cookie login to remain available for web admin")
	}
}

func TestAdminSharedContractLogoutInvalidatesSession(t *testing.T) {
	h := newRuntimeHandler(t)
	loginPayload := []byte(`{"password":"admin-secret"}`)
	loginReq := httptest.NewRequest(http.MethodPost, "/api/admin/auth/login", bytes.NewReader(loginPayload))
	loginRec := httptest.NewRecorder()
	h.Routes().ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("unexpected contract login status: %d body=%s", loginRec.Code, loginRec.Body.String())
	}
	var loginBody struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(bytes.NewReader(loginRec.Body.Bytes())).Decode(&loginBody); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if strings.TrimSpace(loginBody.Token) == "" {
		t.Fatal("expected bearer token from login response")
	}
	logoutReq := httptest.NewRequest(http.MethodPost, "/api/admin/auth/logout", nil)
	logoutReq.Header.Set("Authorization", "Bearer "+loginBody.Token)
	logoutRec := httptest.NewRecorder()
	h.Routes().ServeHTTP(logoutRec, logoutReq)
	if logoutRec.Code != http.StatusOK {
		t.Fatalf("unexpected contract logout status: %d body=%s", logoutRec.Code, logoutRec.Body.String())
	}

	sessionReq := httptest.NewRequest(http.MethodGet, "/api/admin/auth/session", nil)
	sessionReq.Header.Set("Authorization", "Bearer "+loginBody.Token)
	sessionRec := httptest.NewRecorder()
	h.Routes().ServeHTTP(sessionRec, sessionReq)
	if sessionRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected logged out session to return 401, got %d body=%s", sessionRec.Code, sessionRec.Body.String())
	}
}

func newRuntimeHandler(t *testing.T) *Handler {
	t.Helper()
	cfg := testAppConfig()
	cfg.AdminPassword = "admin-secret"
	runtime, err := core.NewRuntimeManager(filepath.Join(t.TempDir(), "admin.json"), cfg)
	if err != nil {
		t.Fatalf("new runtime manager: %v", err)
	}
	return NewHandlerWithRuntime(runtime)
}

func newRuntimeAdminHandler(t *testing.T) (*Handler, *http.Cookie) {
	t.Helper()
	h := newRuntimeHandler(t)
	loginPayload := []byte(`{"password":"admin-secret"}`)
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/api/login", bytes.NewReader(loginPayload))
	loginRec := httptest.NewRecorder()
	h.Routes().ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("unexpected admin login status: %d body=%s", loginRec.Code, loginRec.Body.String())
	}
	res := loginRec.Result()
	defer res.Body.Close()
	for _, cookie := range res.Cookies() {
		if cookie.Name == adminSessionCookieName {
			return h, cookie
		}
	}
	t.Fatal("missing admin session cookie")
	return nil, nil
}

type testAnthropicOnlyProvider struct{}

func (testAnthropicOnlyProvider) ID() string { return "anthropic-only" }

func (testAnthropicOnlyProvider) Capabilities() core.ProviderCapabilities {
	return core.ProviderCapabilities{AnthropicCompatible: true}
}

func (testAnthropicOnlyProvider) Models() []core.ModelInfo {
	return []core.ModelInfo{{Provider: "anthropic-only", PublicModel: "stub-model", UpstreamModel: "stub-model", OwnedBy: "tests"}}
}

func (testAnthropicOnlyProvider) BuildUpstreamPreview(core.UnifiedRequest) map[string]interface{} {
	return map[string]interface{}{"live_enabled": false}
}

func (testAnthropicOnlyProvider) GenerateReply(core.UnifiedRequest) string { return "stub" }
