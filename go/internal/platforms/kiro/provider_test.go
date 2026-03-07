package kiro

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"any2api-go/internal/core"
)

func TestKiroProviderAppliesSystemPromptInjectionAndTruncation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("expected bearer token, got %q", got)
		}
		if got := r.Header.Get("X-Amz-Target"); got != "AmazonCodeWhispererStreamingService.GenerateAssistantResponse" {
			t.Fatalf("unexpected x-amz-target: %q", got)
		}
		var body kiroRequestPayload
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		if got := body.ConversationState.CurrentMessage.UserInputMessage.Content; got != "follow the rules\n\nhi" {
			t.Fatalf("unexpected current content: %q", got)
		}
		if len(body.ConversationState.History) != 0 {
			t.Fatalf("expected no history after truncation, got %#v", body.ConversationState.History)
		}
		_, _ = w.Write(encodeKiroTestStream(kiroTestEvent{
			eventType: "assistantResponseEvent",
			payload:   map[string]interface{}{"content": "trimmed ok"},
		}))
	}))
	defer upstream.Close()

	p := NewProviderWithConfig(core.KiroConfig{
		AccessToken:      "test-token",
		CodeWhispererURL: upstream.URL,
		AmazonQURL:       upstream.URL,
		Request: core.RequestConfig{
			Timeout:            5 * time.Second,
			MaxInputLength:     2,
			SystemPromptInject: "follow the rules",
		},
	}).(*kiroProvider)

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

func TestKiroProviderFallsBackToSecondEndpointOn429(t *testing.T) {
	var firstHits atomic.Int32
	var secondHits atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		firstHits.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("quota exhausted"))
	}))
	defer first.Close()

	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondHits.Add(1)
		_, _ = w.Write(encodeKiroTestStream(kiroTestEvent{
			eventType: "assistantResponseEvent",
			payload:   map[string]interface{}{"content": "fallback ok"},
		}))
	}))
	defer second.Close()

	p := NewProviderWithConfig(core.KiroConfig{
		AccessToken:      "test-token",
		CodeWhispererURL: first.URL,
		AmazonQURL:       second.URL,
		Request:          core.RequestConfig{Timeout: 5 * time.Second, MaxInputLength: core.DefaultCursorMaxInputLength},
	}).(*kiroProvider)

	text, err := p.CompleteOpenAI(context.Background(), core.UnifiedRequest{
		Model:    "claude-sonnet-4.6",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("CompleteOpenAI returned error: %v", err)
	}
	if text != "fallback ok" {
		t.Fatalf("unexpected text: %q", text)
	}
	if firstHits.Load() != 1 || secondHits.Load() != 1 {
		t.Fatalf("expected one hit per endpoint, got first=%d second=%d", firstHits.Load(), secondHits.Load())
	}
}

func TestKiroProviderAutoGeneratesMachineIDWhenMissing(t *testing.T) {
	p := NewProviderWithConfig(core.KiroConfig{
		AccessToken: "test-token",
		Request:     core.RequestConfig{Timeout: 5 * time.Second, MaxInputLength: core.DefaultCursorMaxInputLength},
	}).(*kiroProvider)

	if p.machineID == "" {
		t.Fatal("expected machine id to be auto-generated")
	}
	matched, err := regexp.MatchString("^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$", p.machineID)
	if err != nil {
		t.Fatalf("compile machine id regex: %v", err)
	}
	if !matched {
		t.Fatalf("expected uuid-like machine id, got %q", p.machineID)
	}
	userAgent, amzUserAgent := p.userAgents()
	if !strings.Contains(userAgent, p.machineID) {
		t.Fatalf("expected user-agent to include generated machine id, got %q", userAgent)
	}
	if !strings.Contains(amzUserAgent, p.machineID) {
		t.Fatalf("expected x-amz-user-agent to include generated machine id, got %q", amzUserAgent)
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
