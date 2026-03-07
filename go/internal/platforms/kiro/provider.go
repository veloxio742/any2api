package kiro

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"any2api-go/internal/core"
)

const kiroVersion = "0.7.45"

type kiroProvider struct {
	client            *http.Client
	requestConfig     core.RequestConfig
	accessToken       string
	machineID         string
	preferredEndpoint string
	codeWhispererURL  string
	amazonQURL        string
}

type kiroEndpoint struct {
	name      string
	url       string
	origin    string
	amzTarget string
}

type kiroRequestPayload struct {
	ConversationState kiroConversationState `json:"conversationState"`
}

type kiroConversationState struct {
	ChatTriggerType string               `json:"chatTriggerType"`
	ConversationID  string               `json:"conversationId"`
	CurrentMessage  kiroCurrentMessage   `json:"currentMessage"`
	History         []kiroHistoryMessage `json:"history,omitempty"`
}

type kiroCurrentMessage struct {
	UserInputMessage kiroUserInputMessage `json:"userInputMessage"`
}

type kiroUserInputMessage struct {
	Content string `json:"content"`
	ModelID string `json:"modelId,omitempty"`
	Origin  string `json:"origin"`
}

type kiroHistoryMessage struct {
	UserInputMessage         *kiroUserInputMessage         `json:"userInputMessage,omitempty"`
	AssistantResponseMessage *kiroAssistantResponseMessage `json:"assistantResponseMessage,omitempty"`
}

type kiroAssistantResponseMessage struct {
	Content string `json:"content"`
}

func NewProvider() core.Provider {
	return NewProviderWithConfig(core.KiroConfig{})
}

func NewProviderWithConfig(cfg core.KiroConfig) core.Provider {
	machineID := strings.TrimSpace(cfg.MachineID)
	if machineID == "" {
		machineID = generateKiroMachineID()
	}
	if cfg.CodeWhispererURL == "" {
		cfg.CodeWhispererURL = core.DefaultKiroCodeWhispererURL
	}
	if cfg.AmazonQURL == "" {
		cfg.AmazonQURL = core.DefaultKiroAmazonQURL
	}
	if cfg.Request.Timeout <= 0 {
		cfg.Request.Timeout = time.Duration(core.DefaultCursorTimeoutSeconds) * time.Second
	}
	if cfg.Request.MaxInputLength <= 0 {
		cfg.Request.MaxInputLength = core.DefaultCursorMaxInputLength
	}
	return &kiroProvider{
		client:            &http.Client{Timeout: cfg.Request.Timeout},
		requestConfig:     cfg.Request,
		accessToken:       strings.TrimSpace(cfg.AccessToken),
		machineID:         machineID,
		preferredEndpoint: strings.TrimSpace(strings.ToLower(cfg.PreferredEndpoint)),
		codeWhispererURL:  cfg.CodeWhispererURL,
		amazonQURL:        cfg.AmazonQURL,
	}
}

func (*kiroProvider) ID() string { return "kiro" }

func (*kiroProvider) Capabilities() core.ProviderCapabilities {
	return core.ProviderCapabilities{OpenAICompatible: true, AnthropicCompatible: true, Tools: true, Images: true, MultiAccount: true}
}

func (*kiroProvider) Models() []core.ModelInfo {
	return []core.ModelInfo{{Provider: "kiro", PublicModel: "claude-sonnet-4.6", UpstreamModel: "claude-sonnet-4.6", OwnedBy: "amazonq/kiro"}}
}

func (p *kiroProvider) BuildUpstreamPreview(req core.UnifiedRequest) map[string]interface{} {
	endpoint := p.sortedEndpoints()[0]
	payload := p.buildKiroRequest(req, endpoint.origin)
	return map[string]interface{}{
		"url":                endpoint.url,
		"auth":               "bearer access token + x-amz-user-agent (machine id auto-generated when omitted)",
		"live_enabled":       true,
		"token_configured":   p.accessToken != "",
		"preferred_endpoint": p.preferredEndpoint,
		"payload": map[string]interface{}{
			"protocol":      req.Protocol,
			"model":         payload.ConversationState.CurrentMessage.UserInputMessage.ModelID,
			"history_count": len(payload.ConversationState.History),
		},
	}
}

func (*kiroProvider) GenerateReply(req core.UnifiedRequest) string {
	return "[kiro skeleton] mapped request to Kiro conversation payload"
}

func (p *kiroProvider) CompleteOpenAI(ctx context.Context, req core.UnifiedRequest) (string, error) {
	return core.CollectTextStream(ctx, p.mustStream(ctx, req))
}

func (p *kiroProvider) StreamOpenAI(ctx context.Context, req core.UnifiedRequest) (<-chan core.TextStreamEvent, error) {
	return p.stream(ctx, req)
}

func (p *kiroProvider) CompleteAnthropic(ctx context.Context, req core.UnifiedRequest) (string, error) {
	return core.CollectTextStream(ctx, p.mustStream(ctx, req))
}

func (p *kiroProvider) StreamAnthropic(ctx context.Context, req core.UnifiedRequest) (<-chan core.TextStreamEvent, error) {
	return p.stream(ctx, req)
}

func (p *kiroProvider) mustStream(ctx context.Context, req core.UnifiedRequest) <-chan core.TextStreamEvent {
	events, err := p.stream(ctx, req)
	if err != nil {
		output := make(chan core.TextStreamEvent, 1)
		go func() {
			defer close(output)
			select {
			case <-ctx.Done():
				output <- core.TextStreamEvent{Err: ctx.Err()}
			case output <- core.TextStreamEvent{Err: err}:
			}
		}()
		return output
	}
	return events
}

func (p *kiroProvider) stream(ctx context.Context, req core.UnifiedRequest) (<-chan core.TextStreamEvent, error) {
	resp, err := p.doRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	output := make(chan core.TextStreamEvent, 32)
	go p.consumeEventStream(ctx, resp.Body, output)
	return output, nil
}

func (p *kiroProvider) doRequest(ctx context.Context, req core.UnifiedRequest) (*http.Response, error) {
	if p.accessToken == "" {
		return nil, fmt.Errorf("kiro access token is not configured")
	}

	var lastErr error
	for _, endpoint := range p.sortedEndpoints() {
		payload := p.buildKiroRequest(req, endpoint.origin)
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshal kiro payload: %w", err)
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.url, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("build kiro request: %w", err)
		}
		userAgent, amzUserAgent := p.userAgents()
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "*/*")
		httpReq.Header.Set("Authorization", "Bearer "+p.accessToken)
		httpReq.Header.Set("X-Amz-Target", endpoint.amzTarget)
		httpReq.Header.Set("User-Agent", userAgent)
		httpReq.Header.Set("X-Amz-User-Agent", amzUserAgent)
		httpReq.Header.Set("x-amzn-kiro-agent-mode", "vibe")
		httpReq.Header.Set("x-amzn-codewhisperer-optout", "true")
		httpReq.Header.Set("Amz-Sdk-Request", "attempt=1; max=2")
		httpReq.Header.Set("Amz-Sdk-Invocation-Id", randomKiroID(16))

		resp, err := p.client.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("kiro upstream request failed: %w", err)
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			lastErr = fmt.Errorf("kiro endpoint %s returned 429", endpoint.name)
			continue
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			defer resp.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			lastErr = fmt.Errorf("kiro upstream error: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
			if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
				return nil, lastErr
			}
			continue
		}

		return resp, nil
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("kiro upstream request failed")
}

func (p *kiroProvider) buildKiroRequest(req core.UnifiedRequest, origin string) kiroRequestPayload {
	normalized := core.NormalizeMessages(req, p.requestConfig.SystemPromptInject, p.requestConfig.MaxInputLength)
	modelID := mapKiroModel(req.Model)
	systemPrompt, nonSystem := splitSystemMessages(normalized)
	if len(nonSystem) == 0 {
		nonSystem = []core.Message{{Role: "user", Content: "."}}
	}

	history := make([]kiroHistoryMessage, 0, len(nonSystem))
	currentContent := ""
	anchor := ""
	systemMerged := false

	for i, msg := range nonSystem {
		text := strings.TrimSpace(core.ContentText(msg.Content))
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		isLast := i == len(nonSystem)-1

		if role == "assistant" {
			if text == "" {
				continue
			}
			history = append(history, kiroHistoryMessage{AssistantResponseMessage: &kiroAssistantResponseMessage{Content: text}})
			continue
		}

		if role == "" {
			role = "user"
		}
		if role != "user" {
			if text != "" {
				text = fmt.Sprintf("%s: %s", role, text)
			}
		}
		if !systemMerged && systemPrompt != "" {
			if text == "" {
				text = systemPrompt
			} else {
				text = systemPrompt + "\n\n" + text
			}
			systemMerged = true
		}
		if anchor == "" && strings.TrimSpace(text) != "" {
			anchor = strings.TrimSpace(text)
		}
		if text == "" {
			text = "."
		}
		entry := &kiroUserInputMessage{Content: text, ModelID: modelID, Origin: origin}
		if isLast {
			currentContent = entry.Content
			continue
		}
		history = append(history, kiroHistoryMessage{UserInputMessage: entry})
	}

	if currentContent == "" {
		currentContent = "."
		if !systemMerged && systemPrompt != "" {
			currentContent = systemPrompt + "\n\n" + currentContent
		}
	}
	if anchor == "" {
		anchor = currentContent
	}

	return kiroRequestPayload{
		ConversationState: kiroConversationState{
			ChatTriggerType: "MANUAL",
			ConversationID:  randomKiroConversationID(modelID, anchor),
			CurrentMessage: kiroCurrentMessage{UserInputMessage: kiroUserInputMessage{
				Content: currentContent,
				ModelID: modelID,
				Origin:  origin,
			}},
			History: history,
		},
	}
}

func (p *kiroProvider) consumeEventStream(ctx context.Context, body io.ReadCloser, output chan<- core.TextStreamEvent) {
	defer body.Close()
	defer close(output)

	var lastAssistant string
	var lastReasoning string

	for {
		prelude := make([]byte, 12)
		if _, err := io.ReadFull(body, prelude); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return
			}
			p.emitKiroEvent(ctx, output, core.TextStreamEvent{Err: err})
			return
		}

		totalLength := int(prelude[0])<<24 | int(prelude[1])<<16 | int(prelude[2])<<8 | int(prelude[3])
		headersLength := int(prelude[4])<<24 | int(prelude[5])<<16 | int(prelude[6])<<8 | int(prelude[7])
		if totalLength < 16 {
			continue
		}

		remaining := totalLength - 12
		message := make([]byte, remaining)
		if _, err := io.ReadFull(body, message); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return
			}
			p.emitKiroEvent(ctx, output, core.TextStreamEvent{Err: err})
			return
		}
		if headersLength > len(message)-4 {
			continue
		}

		eventType := extractKiroEventType(message[:headersLength])
		payloadBytes := message[headersLength : len(message)-4]
		if len(payloadBytes) == 0 {
			continue
		}

		var event map[string]interface{}
		if err := json.Unmarshal(payloadBytes, &event); err != nil {
			continue
		}

		var delta string
		switch eventType {
		case "assistantResponseEvent":
			if content, _ := event["content"].(string); content != "" {
				delta = normalizeKiroChunk(content, &lastAssistant)
			}
		case "reasoningContentEvent":
			if content, _ := event["text"].(string); content != "" {
				delta = normalizeKiroChunk(content, &lastReasoning)
			}
		}

		if delta != "" {
			if !p.emitKiroEvent(ctx, output, core.TextStreamEvent{Delta: delta}) {
				return
			}
		}
	}
}

func (p *kiroProvider) emitKiroEvent(ctx context.Context, output chan<- core.TextStreamEvent, event core.TextStreamEvent) bool {
	select {
	case <-ctx.Done():
		select {
		case output <- core.TextStreamEvent{Err: ctx.Err()}:
		default:
		}
		return false
	case output <- event:
		return true
	}
}

func (p *kiroProvider) sortedEndpoints() []kiroEndpoint {
	codeWhisperer := kiroEndpoint{
		name:      "codewhisperer",
		url:       p.codeWhispererURL,
		origin:    "AI_EDITOR",
		amzTarget: "AmazonCodeWhispererStreamingService.GenerateAssistantResponse",
	}
	amazonQ := kiroEndpoint{
		name:      "amazonq",
		url:       p.amazonQURL,
		origin:    "CLI",
		amzTarget: "AmazonQDeveloperStreamingService.SendMessage",
	}
	switch p.preferredEndpoint {
	case "amazonq":
		return []kiroEndpoint{amazonQ, codeWhisperer}
	case "codewhisperer":
		return []kiroEndpoint{codeWhisperer, amazonQ}
	default:
		return []kiroEndpoint{codeWhisperer, amazonQ}
	}
}

func (p *kiroProvider) userAgents() (string, string) {
	if p.machineID == "" {
		return fmt.Sprintf("aws-sdk-js/1.0.27 ua/2.1 os/linux lang/js md/nodejs#22.21.1 api/codewhispererstreaming#1.0.27 m/E KiroIDE-%s", kiroVersion),
			fmt.Sprintf("aws-sdk-js/1.0.27 KiroIDE %s", kiroVersion)
	}
	return fmt.Sprintf("aws-sdk-js/1.0.27 ua/2.1 os/linux lang/js md/nodejs#22.21.1 api/codewhispererstreaming#1.0.27 m/E KiroIDE-%s-%s", kiroVersion, p.machineID),
		fmt.Sprintf("aws-sdk-js/1.0.27 KiroIDE %s %s", kiroVersion, p.machineID)
}

func splitSystemMessages(messages []core.Message) (string, []core.Message) {
	nonSystem := make([]core.Message, 0, len(messages))
	var systemParts []string
	for _, msg := range messages {
		if strings.EqualFold(msg.Role, "system") {
			if text := strings.TrimSpace(core.ContentText(msg.Content)); text != "" {
				systemParts = append(systemParts, text)
			}
			continue
		}
		nonSystem = append(nonSystem, msg)
	}
	return strings.Join(systemParts, "\n\n"), nonSystem
}

func mapKiroModel(model string) string {
	lower := strings.ToLower(strings.TrimSpace(model))
	switch {
	case lower == "", strings.Contains(lower, "claude-sonnet-4.6"), strings.Contains(lower, "claude-sonnet-4-6"):
		return "claude-sonnet-4.6"
	case strings.Contains(lower, "claude-sonnet-4.5"), strings.Contains(lower, "claude-sonnet-4-5"), strings.Contains(lower, "claude-3-5-sonnet"), strings.Contains(lower, "gpt-4o"), strings.Contains(lower, "gpt-4"):
		return "claude-sonnet-4.5"
	case strings.HasPrefix(lower, "claude-"):
		return model
	default:
		return "claude-sonnet-4.6"
	}
}

func randomKiroConversationID(modelID, anchor string) string {
	if strings.TrimSpace(modelID+":"+anchor) == "" {
		return randomKiroID(16)
	}
	return randomKiroID(16)
}

func generateKiroMachineID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("kiro-%d", time.Now().UnixNano())
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}

func randomKiroID(byteLen int) string {
	buf := make([]byte, byteLen)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func normalizeKiroChunk(chunk string, previous *string) string {
	if chunk == "" {
		return ""
	}
	prev := *previous
	if prev == "" {
		*previous = chunk
		return chunk
	}
	if chunk == prev {
		return ""
	}
	if strings.HasPrefix(chunk, prev) {
		delta := chunk[len(prev):]
		*previous = chunk
		return delta
	}
	if strings.HasPrefix(prev, chunk) {
		return ""
	}
	maxOverlap := 0
	maxLen := len(prev)
	if len(chunk) < maxLen {
		maxLen = len(chunk)
	}
	for i := maxLen; i > 0; i-- {
		if strings.HasSuffix(prev, chunk[:i]) {
			maxOverlap = i
			break
		}
	}
	*previous = chunk
	if maxOverlap > 0 {
		return chunk[maxOverlap:]
	}
	return chunk
}

func extractKiroEventType(headers []byte) string {
	offset := 0
	for offset < len(headers) {
		nameLen := int(headers[offset])
		offset++
		if offset+nameLen > len(headers) {
			break
		}
		name := string(headers[offset : offset+nameLen])
		offset += nameLen
		if offset >= len(headers) {
			break
		}
		valueType := headers[offset]
		offset++
		if valueType == 7 {
			if offset+2 > len(headers) {
				break
			}
			valueLen := int(headers[offset])<<8 | int(headers[offset+1])
			offset += 2
			if offset+valueLen > len(headers) {
				break
			}
			value := string(headers[offset : offset+valueLen])
			offset += valueLen
			if name == ":event-type" {
				return value
			}
			continue
		}
		skipSizes := map[byte]int{0: 0, 1: 0, 2: 1, 3: 2, 4: 4, 5: 8, 8: 8, 9: 16}
		if valueType == 6 {
			if offset+2 > len(headers) {
				break
			}
			valueLen := int(headers[offset])<<8 | int(headers[offset+1])
			offset += 2 + valueLen
			continue
		}
		skip, ok := skipSizes[valueType]
		if !ok {
			break
		}
		offset += skip
	}
	return ""
}
