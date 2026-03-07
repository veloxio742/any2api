package grok

import (
	"strings"
	"testing"

	"any2api-go/internal/core"
)

func TestGrokProviderFlattensMessages(t *testing.T) {
	p := NewProviderWithConfig(core.GrokConfig{
		Request: core.RequestConfig{
			MaxInputLength:     core.DefaultCursorMaxInputLength,
			SystemPromptInject: "follow the rules",
		},
	}).(*grokProvider)

	got := p.flattenMessages(core.UnifiedRequest{
		Model:  "grok-4",
		System: "be precise",
		Messages: []core.Message{
			{Role: "assistant", Content: "earlier reply"},
			{Role: "user", Content: "latest question"},
		},
	})
	want := "system: be precise\nfollow the rules\n\nassistant: earlier reply\n\nlatest question"
	if got != want {
		t.Fatalf("unexpected flattened messages:\nwant=%q\n got=%q", want, got)
	}
}

func TestGrokStreamFilterHandlesSplitToolCard(t *testing.T) {
	filter := &grokStreamFilter{}
	first := filter.filter("hello<xai:tool_usage_card><xai:tool_name>web_search</xai:tool_name>")
	if first != "hello" {
		t.Fatalf("unexpected first filtered chunk: %q", first)
	}
	second := filter.filter("<xai:tool_args>{\"query\":\"q\"}</xai:tool_args></xai:tool_usage_card>world")
	if !strings.Contains(second, "[web_search] {\"query\":\"q\"}") {
		t.Fatalf("expected tool summary in second chunk, got %q", second)
	}
	if !strings.Contains(second, "world") {
		t.Fatalf("expected trailing text to remain, got %q", second)
	}
}

func TestGrokProviderUsesDefaultsForOptionalHeaders(t *testing.T) {
	p := NewProviderWithConfig(core.GrokConfig{
		CookieToken: "test-token",
		Request:     core.RequestConfig{MaxInputLength: core.DefaultCursorMaxInputLength},
	}).(*grokProvider)

	headers := p.headers()
	if got := headers["User-Agent"]; got != core.DefaultGrokUserAgent {
		t.Fatalf("expected default user-agent, got %q", got)
	}
	if got := headers["Origin"]; got != core.DefaultGrokOrigin {
		t.Fatalf("expected default origin, got %q", got)
	}
	if got := headers["Referer"]; got != core.DefaultGrokReferer {
		t.Fatalf("expected default referer, got %q", got)
	}
	if got := headers["Cookie"]; got != "sso=test-token; sso-rw=test-token" {
		t.Fatalf("expected normalized sso cookie, got %q", got)
	}
}
