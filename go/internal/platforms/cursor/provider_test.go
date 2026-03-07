package cursor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"any2api-go/internal/core"
)

func TestCursorProviderUsesComputedXIsHumanWithoutManualCookie(t *testing.T) {
	var scriptRequests atomic.Int32
	var upstreamRequests atomic.Int32
	script := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scriptRequests.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`window.V_C=[async()=>"ignored"];`))
	}))
	defer script.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequests.Add(1)
		if got := r.Header.Get("X-Is-Human"); got != "computed-human-token" {
			t.Fatalf("expected computed x-is-human header, got %q", got)
		}
		if got := r.Header.Get("Cookie"); got != "" {
			t.Fatalf("expected no cookie header, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"hello"}`))
	}))
	defer upstream.Close()

	p := newTestCursorProvider(upstream.URL, script.URL)
	p.jsRunner = func(string) (string, error) { return `"computed-human-token"`, nil }

	text, err := p.CompleteOpenAI(context.Background(), core.UnifiedRequest{Model: "claude-sonnet-4.6", Messages: []core.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("CompleteOpenAI returned error: %v", err)
	}
	if text != "hello" {
		t.Fatalf("unexpected text: %q", text)
	}
	if scriptRequests.Load() != 1 {
		t.Fatalf("expected one script fetch, got %d", scriptRequests.Load())
	}
	if upstreamRequests.Load() != 1 {
		t.Fatalf("expected one upstream request, got %d", upstreamRequests.Load())
	}
}

func TestCursorProviderRetriesOnForbiddenAndClearsScriptCache(t *testing.T) {
	var scriptRequests atomic.Int32
	var upstreamRequests atomic.Int32
	script := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scriptRequests.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`window.V_C=[async()=>"ignored"];`))
	}))
	defer script.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := upstreamRequests.Add(1)
		if attempt == 1 {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("Access denied"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"retried ok"}`))
	}))
	defer upstream.Close()

	p := newTestCursorProvider(upstream.URL, script.URL)
	p.jsRunner = func(string) (string, error) {
		return fmt.Sprintf(`"retry-token-%d"`, scriptRequests.Load()), nil
	}

	text, err := p.CompleteOpenAI(context.Background(), core.UnifiedRequest{Model: "claude-sonnet-4.6", Messages: []core.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("CompleteOpenAI returned error: %v", err)
	}
	if text != "retried ok" {
		t.Fatalf("unexpected text: %q", text)
	}
	if upstreamRequests.Load() != 2 {
		t.Fatalf("expected two upstream attempts, got %d", upstreamRequests.Load())
	}
	if scriptRequests.Load() != 2 {
		t.Fatalf("expected script to be re-fetched after 403, got %d", scriptRequests.Load())
	}
}

func TestCursorProviderFallsBackToGeneratedTokenWhenJSFails(t *testing.T) {
	script := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`window.V_C=[async()=>"ignored"];`))
	}))
	defer script.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-Is-Human")
		if len(got) != 64 {
			t.Fatalf("expected fallback x-is-human token length 64, got %d (%q)", len(got), got)
		}
		if strings.Contains(strings.ToLower(got), "error") {
			t.Fatalf("unexpected fallback token content: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"fallback ok"}`))
	}))
	defer upstream.Close()

	p := newTestCursorProvider(upstream.URL, script.URL)
	p.jsRunner = func(string) (string, error) { return "", fmt.Errorf("node failed") }

	text, err := p.CompleteOpenAI(context.Background(), core.UnifiedRequest{Model: "claude-sonnet-4.6", Messages: []core.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("CompleteOpenAI returned error: %v", err)
	}
	if text != "fallback ok" {
		t.Fatalf("unexpected text: %q", text)
	}
}

func TestCursorProviderAppliesSystemPromptInjectionAndTruncation(t *testing.T) {
	script := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`window.V_C=[async()=>"ignored"];`))
	}))
	defer script.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
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
		if len(body.Messages) != 2 {
			t.Fatalf("expected injected system message and latest user message, got %#v", body.Messages)
		}
		if body.Messages[0].Role != "system" || body.Messages[0].Parts[0].Text != "follow the rules" {
			t.Fatalf("unexpected injected system message: %#v", body.Messages[0])
		}
		if body.Messages[1].Role != "user" || body.Messages[1].Parts[0].Text != "hi" {
			t.Fatalf("expected only latest user message to remain after truncation, got %#v", body.Messages[1])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"trimmed ok"}`))
	}))
	defer upstream.Close()

	p := newTestCursorProvider(upstream.URL, script.URL)
	p.requestConfig = core.RequestConfig{MaxInputLength: 2, SystemPromptInject: "follow the rules"}

	text, err := p.CompleteOpenAI(context.Background(), core.UnifiedRequest{
		Model: "claude-sonnet-4.6",
		Messages: []core.Message{
			{Role: "user", Content: "this is too long"},
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("CompleteOpenAI returned error: %v", err)
	}
	if text != "trimmed ok" {
		t.Fatalf("unexpected text: %q", text)
	}
}

func newTestCursorProvider(apiURL, scriptURL string) *cursorProvider {
	return &cursorProvider{
		client:          &http.Client{Timeout: 5 * time.Second},
		requestConfig:   core.RequestConfig{Timeout: 5 * time.Second, MaxInputLength: core.DefaultCursorMaxInputLength},
		apiURL:          apiURL,
		scriptURL:       scriptURL,
		webGLVendor:     core.DefaultCursorWebGLVendor,
		webGLRenderer:   core.DefaultCursorWebGLRenderer,
		mainJS:          fallbackCursorMainJS,
		envJS:           fallbackCursorEnvJS,
		headerGenerator: newCursorHeaderGenerator(),
		jsRunner:        func(string) (string, error) { return `"test-token"`, nil },
		sleep:           func(time.Duration) {},
	}
}
