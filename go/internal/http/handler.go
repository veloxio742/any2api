package http

import (
	"encoding/json"
	"fmt"
	stdhttp "net/http"
	"strings"
	"time"

	"any2api-go/internal/core"
	"any2api-go/internal/platforms"
)

type Handler struct {
	registry *core.Registry
	cfg      core.AppConfig
	runtime  *core.RuntimeManager
	sessions *adminSessionStore
}

func NewHandler(registry *core.Registry, cfg core.AppConfig) *Handler {
	return &Handler{registry: registry, cfg: cfg}
}

func NewHandlerWithRuntime(runtime *core.RuntimeManager) *Handler {
	return &Handler{runtime: runtime, sessions: newAdminSessionStore()}
}

func (h *Handler) Routes() stdhttp.Handler {
	mux := stdhttp.NewServeMux()
	mux.HandleFunc("/health", h.health)
	mux.HandleFunc("/v1/models", h.requireAPIKey(h.models))
	mux.HandleFunc("/v1/chat/completions", h.requireAPIKey(h.openAIChat))
	mux.HandleFunc("/v1/messages", h.requireAPIKey(h.anthropicMessages))
	if h.runtime != nil {
		h.registerAdminRoutes(mux)
	}
	return mux
}

func (h *Handler) health(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
	h.writeJSON(w, 200, map[string]interface{}{"status": "ok", "project": "any2api-go"})
}

func (h *Handler) models(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	models, err := h.currentRegistry().Models(r.URL.Query().Get("provider"))
	if err != nil {
		h.writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	data := make([]map[string]interface{}, 0, len(models))
	for _, model := range models {
		data = append(data, map[string]interface{}{"id": model.PublicModel, "object": "model", "owned_by": model.OwnedBy, "provider": model.Provider, "upstream_model": model.UpstreamModel})
	}
	h.writeJSON(w, 200, map[string]interface{}{"object": "list", "data": data})
}

func (h *Handler) openAIChat(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var req core.OpenAIChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeJSON(w, 400, map[string]string{"error": "invalid json"})
		return
	}
	provider, err := h.currentRegistry().Resolve(req.Provider)
	if err != nil {
		h.writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	if !provider.Capabilities().OpenAICompatible {
		h.writeJSON(w, 400, map[string]string{"error": fmt.Sprintf("provider %q does not support OpenAI compatible endpoint", provider.ID())})
		return
	}
	w.Header().Set("X-Newplatform2API-Provider", provider.ID())
	unified := core.UnifiedRequest{ProviderHint: req.Provider, Protocol: "openai", Model: req.Model, Messages: req.Messages, Stream: req.Stream, ProviderOptions: h.providerOptionsFromHeaders(r)}
	if req.Stream {
		if streamProvider, ok := provider.(core.OpenAIStreamProvider); ok {
			events, err := streamProvider.StreamOpenAI(r.Context(), unified)
			if err != nil {
				h.writeJSON(w, 502, map[string]string{"error": err.Error()})
				return
			}
			h.writeOpenAIEventStream(w, req.Model, events)
			return
		}
	}
	reply := provider.GenerateReply(unified)
	if upstreamProvider, ok := provider.(core.OpenAIChatProvider); ok {
		text, err := upstreamProvider.CompleteOpenAI(r.Context(), unified)
		if err != nil {
			h.writeJSON(w, 502, map[string]string{"error": err.Error()})
			return
		}
		reply = text
	}
	if req.Stream {
		h.writeOpenAIStream(w, reply)
		return
	}
	h.writeJSON(w, 200, map[string]interface{}{"id": fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()), "object": "chat.completion", "created": time.Now().Unix(), "model": req.Model, "choices": []map[string]interface{}{{"index": 0, "message": map[string]interface{}{"role": "assistant", "content": reply}, "finish_reason": "stop"}}})
}

func (h *Handler) anthropicMessages(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var req core.AnthropicMessagesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeJSON(w, 400, map[string]string{"error": "invalid json"})
		return
	}
	provider, err := h.currentRegistry().Resolve(req.Provider)
	if err != nil {
		h.writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	if !provider.Capabilities().AnthropicCompatible {
		h.writeJSON(w, 400, map[string]string{"error": fmt.Sprintf("provider %q does not support Anthropic compatible endpoint", provider.ID())})
		return
	}
	w.Header().Set("X-Newplatform2API-Provider", provider.ID())
	unified := core.UnifiedRequest{ProviderHint: req.Provider, Protocol: "anthropic", Model: req.Model, Messages: req.Messages, System: req.System, Stream: req.Stream, ProviderOptions: h.providerOptionsFromHeaders(r)}
	if req.Stream {
		if streamProvider, ok := provider.(core.AnthropicStreamProvider); ok {
			events, err := streamProvider.StreamAnthropic(r.Context(), unified)
			if err != nil {
				h.writeJSON(w, 502, map[string]string{"error": err.Error()})
				return
			}
			h.writeAnthropicEventStream(w, req.Model, events)
			return
		}
	}
	reply := provider.GenerateReply(unified)
	if upstreamProvider, ok := provider.(core.AnthropicMessagesProvider); ok {
		text, err := upstreamProvider.CompleteAnthropic(r.Context(), unified)
		if err != nil {
			h.writeJSON(w, 502, map[string]string{"error": err.Error()})
			return
		}
		reply = text
	}
	if req.Stream {
		h.writeAnthropicStream(w, req.Model, reply)
		return
	}
	h.writeJSON(w, 200, map[string]interface{}{"id": fmt.Sprintf("msg_%d", time.Now().UnixNano()), "type": "message", "role": "assistant", "model": req.Model, "content": []map[string]interface{}{{"type": "text", "text": reply}}, "stop_reason": "end_turn"})
}

func (h *Handler) writeJSON(w stdhttp.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (h *Handler) providerOptionsFromHeaders(r *stdhttp.Request) map[string]string {
	options := map[string]string{}
	if value := strings.TrimSpace(r.Header.Get("X-Orchids-Client-Cookie")); value != "" {
		options["orchids_client_cookie"] = value
	}
	if value := strings.TrimSpace(r.Header.Get("X-Orchids-Client-UAT")); value != "" {
		options["orchids_client_uat"] = value
	}
	if value := strings.TrimSpace(r.Header.Get("X-Orchids-Session-Id")); value != "" {
		options["orchids_session_id"] = value
	}
	if value := strings.TrimSpace(r.Header.Get("X-Orchids-Project-Id")); value != "" {
		options["orchids_project_id"] = value
	}
	if value := strings.TrimSpace(r.Header.Get("X-Orchids-User-Id")); value != "" {
		options["orchids_user_id"] = value
	}
	if value := strings.TrimSpace(r.Header.Get("X-Orchids-Email")); value != "" {
		options["orchids_email"] = value
	}
	if value := strings.TrimSpace(r.Header.Get("X-Orchids-Agent-Mode")); value != "" {
		options["orchids_agent_mode"] = value
	}
	if len(options) == 0 {
		return nil
	}
	return options
}

func (h *Handler) requireAPIKey(next stdhttp.HandlerFunc) stdhttp.HandlerFunc {
	return func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if strings.TrimSpace(h.currentConfig().APIKey) == "" {
			next(w, r)
			return
		}
		authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
		if !strings.HasPrefix(authHeader, "Bearer ") {
			h.writeJSON(w, stdhttp.StatusUnauthorized, map[string]interface{}{
				"error": map[string]string{
					"message": "Missing or invalid authorization header",
					"type":    "authentication_error",
					"code":    "missing_auth",
				},
			})
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
		if token != h.currentConfig().APIKey {
			h.writeJSON(w, stdhttp.StatusUnauthorized, map[string]interface{}{
				"error": map[string]string{
					"message": "Invalid API key",
					"type":    "authentication_error",
					"code":    "invalid_api_key",
				},
			})
			return
		}
		next(w, r)
	}
}

func (h *Handler) currentConfig() core.AppConfig {
	if h.runtime != nil {
		return h.runtime.CurrentAppConfig()
	}
	return h.cfg
}

func (h *Handler) currentRegistry() *core.Registry {
	if h.runtime != nil {
		return platforms.DefaultRegistry(h.runtime.CurrentAppConfig())
	}
	return h.registry
}

func (h *Handler) writeOpenAIStream(w stdhttp.ResponseWriter, text string) {
	h.writeEventStreamHeaders(w)
	_, _ = fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-skeleton\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":%q},\"finish_reason\":null}]}\n\n", text)
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	h.flush(w)
}

func (h *Handler) writeOpenAIEventStream(w stdhttp.ResponseWriter, model string, events <-chan core.TextStreamEvent) {
	if model == "" {
		model = "auto"
	}
	streamID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	h.writeEventStreamHeaders(w)
	_, _ = fmt.Fprintf(w, "data: {\"id\":%q,\"object\":\"chat.completion.chunk\",\"created\":%d,\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n", streamID, time.Now().Unix(), model)
	h.flush(w)
	for event := range events {
		if event.Err != nil {
			break
		}
		if event.Delta == "" {
			continue
		}
		_, _ = fmt.Fprintf(w, "data: {\"id\":%q,\"object\":\"chat.completion.chunk\",\"created\":%d,\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{\"content\":%q},\"finish_reason\":null}]}\n\n", streamID, time.Now().Unix(), model, event.Delta)
		h.flush(w)
	}
	_, _ = fmt.Fprintf(w, "data: {\"id\":%q,\"object\":\"chat.completion.chunk\",\"created\":%d,\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n", streamID, time.Now().Unix(), model)
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	h.flush(w)
}

func (h *Handler) writeAnthropicStream(w stdhttp.ResponseWriter, model string, text string) {
	output := make(chan core.TextStreamEvent, 1)
	output <- core.TextStreamEvent{Delta: text}
	close(output)
	h.writeAnthropicEventStream(w, model, output)
}

func (h *Handler) writeAnthropicEventStream(w stdhttp.ResponseWriter, model string, events <-chan core.TextStreamEvent) {
	if model == "" {
		model = "auto"
	}
	messageID := fmt.Sprintf("msg_%d", time.Now().UnixNano())
	h.writeEventStreamHeaders(w)
	_, _ = fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":%q,\"type\":\"message\",\"role\":\"assistant\",\"model\":%q,\"content\":[]}}\n\n", messageID, model)
	_, _ = fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
	h.flush(w)
	for event := range events {
		if event.Err != nil {
			_, _ = fmt.Fprintf(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":%q}}\n\n", event.Err.Error())
			h.flush(w)
			return
		}
		if event.Delta == "" {
			continue
		}
		_, _ = fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%q}}\n\n", event.Delta)
		h.flush(w)
	}
	_, _ = fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
	_, _ = fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null}}\n\n")
	_, _ = fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	h.flush(w)
}

func (h *Handler) writeEventStreamHeaders(w stdhttp.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
}

func (h *Handler) flush(w stdhttp.ResponseWriter) {
	if flusher, ok := w.(stdhttp.Flusher); ok {
		flusher.Flush()
	}
}
