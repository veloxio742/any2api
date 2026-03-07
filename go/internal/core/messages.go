package core

import (
	"encoding/json"
	"strings"
)

func NormalizeMessages(req UnifiedRequest, systemPromptInject string, maxInputLength int) []Message {
	messages := MergeSystemMessage(req.Messages, req.System)
	messages = TruncateMessages(messages, maxInputLength)
	return InjectSystemPrompt(messages, systemPromptInject)
}

func MergeSystemMessage(messages []Message, system interface{}) []Message {
	systemText := ContentText(system)
	cloned := cloneMessages(messages)
	if systemText == "" {
		return cloned
	}
	output := make([]Message, 0, len(cloned)+1)
	output = append(output, Message{Role: "system", Content: systemText})
	output = append(output, cloned...)
	return output
}

func InjectSystemPrompt(messages []Message, systemPromptInject string) []Message {
	inject := strings.TrimSpace(systemPromptInject)
	output := cloneMessages(messages)
	if inject == "" {
		return output
	}
	if len(output) > 0 && strings.EqualFold(output[0].Role, "system") {
		existing := ContentText(output[0].Content)
		if existing == "" {
			output[0].Content = inject
		} else {
			output[0].Content = existing + "\n" + inject
		}
		return output
	}
	return append([]Message{{Role: "system", Content: inject}}, output...)
}

func TruncateMessages(messages []Message, maxInputLength int) []Message {
	output := cloneMessages(messages)
	if len(output) == 0 || maxInputLength <= 0 {
		return output
	}

	total := 0
	for _, msg := range output {
		total += len(ContentText(msg.Content))
	}
	if total <= maxInputLength {
		return output
	}

	result := make([]Message, 0, len(output))
	startIdx := 0
	if strings.EqualFold(output[0].Role, "system") {
		result = append(result, output[0])
		maxInputLength -= len(ContentText(output[0].Content))
		if maxInputLength < 0 {
			maxInputLength = 0
		}
		startIdx = 1
	}

	current := 0
	collected := make([]Message, 0, len(output)-startIdx)
	for i := len(output) - 1; i >= startIdx; i-- {
		text := ContentText(output[i].Content)
		if text == "" {
			continue
		}
		if current+len(text) > maxInputLength {
			continue
		}
		collected = append(collected, output[i])
		current += len(text)
	}

	for i, j := 0, len(collected)-1; i < j; i, j = i+1, j-1 {
		collected[i], collected[j] = collected[j], collected[i]
	}
	return append(result, collected...)
}

func ContentText(content interface{}) string {
	if content == nil {
		return ""
	}
	switch content := content.(type) {
	case string:
		return content
	case []interface{}:
		var text strings.Builder
		for _, item := range content {
			part, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			if partType, _ := part["type"].(string); partType != "text" {
				continue
			}
			if value, _ := part["text"].(string); value != "" {
				text.WriteString(value)
			}
		}
		return text.String()
	default:
		encoded, err := json.Marshal(content)
		if err != nil {
			return ""
		}
		return string(encoded)
	}
}

func cloneMessages(messages []Message) []Message {
	if len(messages) == 0 {
		return nil
	}
	output := make([]Message, len(messages))
	copy(output, messages)
	return output
}
