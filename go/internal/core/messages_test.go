package core

import "testing"

func TestNormalizeMessagesTruncatesBeforeInjectingSystemPrompt(t *testing.T) {
	messages := NormalizeMessages(UnifiedRequest{
		Messages: []Message{
			{Role: "user", Content: "this is too long"},
			{Role: "user", Content: "hi"},
		},
	}, "follow the rules", 2)

	if len(messages) != 2 {
		t.Fatalf("expected injected system prompt and latest user message, got %#v", messages)
	}
	if messages[0].Role != "system" || ContentText(messages[0].Content) != "follow the rules" {
		t.Fatalf("unexpected system prompt message: %#v", messages[0])
	}
	if messages[1].Role != "user" || ContentText(messages[1].Content) != "hi" {
		t.Fatalf("unexpected truncated user message: %#v", messages[1])
	}
}

func TestNormalizeMessagesCountsExplicitSystemMessageInTruncation(t *testing.T) {
	messages := NormalizeMessages(UnifiedRequest{
		System: "sys",
		Messages: []Message{
			{Role: "user", Content: "hello"},
			{Role: "user", Content: "ok"},
		},
	}, "", 5)

	if len(messages) != 2 {
		t.Fatalf("expected explicit system message and most recent fitting user message, got %#v", messages)
	}
	if messages[0].Role != "system" || ContentText(messages[0].Content) != "sys" {
		t.Fatalf("unexpected explicit system message: %#v", messages[0])
	}
	if messages[1].Role != "user" || ContentText(messages[1].Content) != "ok" {
		t.Fatalf("unexpected truncated user message: %#v", messages[1])
	}
}
