package grok

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"any2api-go/internal/core"
)

var (
	grokToolUsageCardRE = regexp.MustCompile(`(?s)<xai:tool_usage_card[^>]*>.*?</xai:tool_usage_card>`)
	grokToolNameRE      = regexp.MustCompile(`(?s)<xai:tool_name>(.*?)</xai:tool_name>`)
	grokToolArgsRE      = regexp.MustCompile(`(?s)<xai:tool_args>(.*?)</xai:tool_args>`)
	grokCDATARE         = regexp.MustCompile(`(?s)<!\[CDATA\[(.*?)\]\]>`)
	grokRolloutRE       = regexp.MustCompile(`(?s)<rolloutId>.*?</rolloutId>`)
	grokSpecialTagRE    = regexp.MustCompile(`</?xai:[^>]+>`)
)

type grokProvider struct {
	client        *http.Client
	requestConfig core.RequestConfig
	apiURL        string
	cookieToken   string
	userAgent     string
	origin        string
	referer       string
}

type grokRequestPayload struct {
	DeviceEnvInfo struct {
		DarkModeEnabled  bool `json:"darkModeEnabled"`
		DevicePixelRatio int  `json:"devicePixelRatio"`
		ScreenWidth      int  `json:"screenWidth"`
		ScreenHeight     int  `json:"screenHeight"`
		ViewportWidth    int  `json:"viewportWidth"`
		ViewportHeight   int  `json:"viewportHeight"`
	} `json:"deviceEnvInfo"`
	DisableMemory               bool                   `json:"disableMemory"`
	DisableSearch               bool                   `json:"disableSearch"`
	DisableSelfHarmShortCircuit bool                   `json:"disableSelfHarmShortCircuit"`
	DisableTextFollowUps        bool                   `json:"disableTextFollowUps"`
	EnableImageGeneration       bool                   `json:"enableImageGeneration"`
	EnableImageStreaming        bool                   `json:"enableImageStreaming"`
	EnableSideBySide            bool                   `json:"enableSideBySide"`
	FileAttachments             []string               `json:"fileAttachments"`
	ForceConcise                bool                   `json:"forceConcise"`
	ForceSideBySide             bool                   `json:"forceSideBySide"`
	ImageAttachments            []string               `json:"imageAttachments"`
	ImageGenerationCount        int                    `json:"imageGenerationCount"`
	IsAsyncChat                 bool                   `json:"isAsyncChat"`
	IsReasoning                 bool                   `json:"isReasoning"`
	Message                     string                 `json:"message"`
	ModelName                   string                 `json:"modelName"`
	ResponseMetadata            map[string]interface{} `json:"responseMetadata"`
	ReturnImageBytes            bool                   `json:"returnImageBytes"`
	ReturnRawGrokInXaiRequest   bool                   `json:"returnRawGrokInXaiRequest"`
	SendFinalMetadata           bool                   `json:"sendFinalMetadata"`
	Temporary                   bool                   `json:"temporary"`
	ToolOverrides               map[string]interface{} `json:"toolOverrides"`
}

type grokStreamFilter struct {
	toolCardOpen bool
	buffer       strings.Builder
}

func NewProvider() core.Provider {
	return NewProviderWithConfig(core.GrokConfig{})
}

func NewProviderWithConfig(cfg core.GrokConfig) core.Provider {
	if cfg.APIURL == "" {
		cfg.APIURL = core.DefaultGrokAPIURL
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = core.DefaultGrokUserAgent
	}
	if cfg.Origin == "" {
		cfg.Origin = core.DefaultGrokOrigin
	}
	if cfg.Referer == "" {
		cfg.Referer = core.DefaultGrokReferer
	}
	if cfg.Request.Timeout <= 0 {
		cfg.Request.Timeout = time.Duration(core.DefaultCursorTimeoutSeconds) * time.Second
	}
	if cfg.Request.MaxInputLength <= 0 {
		cfg.Request.MaxInputLength = core.DefaultCursorMaxInputLength
	}
	return &grokProvider{
		client:        &http.Client{Timeout: cfg.Request.Timeout},
		requestConfig: cfg.Request,
		apiURL:        cfg.APIURL,
		cookieToken:   strings.TrimSpace(cfg.CookieToken),
		userAgent:     strings.TrimSpace(cfg.UserAgent),
		origin:        strings.TrimSpace(cfg.Origin),
		referer:       strings.TrimSpace(cfg.Referer),
	}
}

func (*grokProvider) ID() string { return "grok" }

func (*grokProvider) Capabilities() core.ProviderCapabilities {
	return core.ProviderCapabilities{OpenAICompatible: true, MultiAccount: true}
}

func (*grokProvider) Models() []core.ModelInfo {
	return []core.ModelInfo{{Provider: "grok", PublicModel: "grok-4", UpstreamModel: "grok-4", OwnedBy: "xai"}}
}

func (p *grokProvider) BuildUpstreamPreview(req core.UnifiedRequest) map[string]interface{} {
	payload := p.buildPayload(req)
	return map[string]interface{}{
		"url":               p.apiURL,
		"auth":              "grok sso cookie token",
		"live_enabled":      true,
		"cookie_configured": p.cookieToken != "",
		"payload": map[string]interface{}{
			"model":         payload.ModelName,
			"message_len":   len(payload.Message),
			"message_count": len(req.Messages),
		},
	}
}

func (*grokProvider) GenerateReply(req core.UnifiedRequest) string {
	return "[grok skeleton] mapped request to Grok chat/image capable flow"
}

func (p *grokProvider) CompleteOpenAI(ctx context.Context, req core.UnifiedRequest) (string, error) {
	return core.CollectTextStream(ctx, p.mustStream(ctx, req))
}

func (p *grokProvider) StreamOpenAI(ctx context.Context, req core.UnifiedRequest) (<-chan core.TextStreamEvent, error) {
	return p.stream(ctx, req)
}

func (p *grokProvider) mustStream(ctx context.Context, req core.UnifiedRequest) <-chan core.TextStreamEvent {
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

func (p *grokProvider) stream(ctx context.Context, req core.UnifiedRequest) (<-chan core.TextStreamEvent, error) {
	resp, err := p.doRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	output := make(chan core.TextStreamEvent, 32)
	go p.consumeStream(ctx, resp.Body, output)
	return output, nil
}

func (p *grokProvider) doRequest(ctx context.Context, req core.UnifiedRequest) (*http.Response, error) {
	if p.cookieToken == "" {
		return nil, fmt.Errorf("grok cookie token is not configured")
	}
	payload := p.buildPayload(req)
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal grok payload: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.apiURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build grok request: %w", err)
	}
	for key, value := range p.headers() {
		httpReq.Header.Set(key, value)
	}
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("grok upstream request failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("grok upstream error: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return resp, nil
}

func (p *grokProvider) buildPayload(req core.UnifiedRequest) grokRequestPayload {
	payload := grokRequestPayload{
		DisableMemory:               false,
		DisableSearch:               false,
		DisableSelfHarmShortCircuit: false,
		DisableTextFollowUps:        false,
		EnableImageGeneration:       true,
		EnableImageStreaming:        true,
		EnableSideBySide:            true,
		FileAttachments:             []string{},
		ForceConcise:                false,
		ForceSideBySide:             false,
		ImageAttachments:            []string{},
		ImageGenerationCount:        2,
		IsAsyncChat:                 false,
		IsReasoning:                 false,
		Message:                     p.flattenMessages(req),
		ModelName:                   mapGrokModel(req.Model),
		ResponseMetadata: map[string]interface{}{
			"requestModelDetails": map[string]interface{}{"modelId": mapGrokModel(req.Model)},
		},
		ReturnImageBytes:          false,
		ReturnRawGrokInXaiRequest: false,
		SendFinalMetadata:         true,
		Temporary:                 false,
		ToolOverrides:             map[string]interface{}{},
	}
	payload.DeviceEnvInfo.DarkModeEnabled = false
	payload.DeviceEnvInfo.DevicePixelRatio = 2
	payload.DeviceEnvInfo.ScreenWidth = 2056
	payload.DeviceEnvInfo.ScreenHeight = 1329
	payload.DeviceEnvInfo.ViewportWidth = 2056
	payload.DeviceEnvInfo.ViewportHeight = 1083
	return payload
}

func (p *grokProvider) flattenMessages(req core.UnifiedRequest) string {
	normalized := core.NormalizeMessages(req, p.requestConfig.SystemPromptInject, p.requestConfig.MaxInputLength)
	type messagePart struct {
		role string
		text string
	}
	parts := make([]messagePart, 0, len(normalized))
	for _, msg := range normalized {
		text := strings.TrimSpace(extractGrokMessageText(msg.Content))
		if text == "" {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		if role == "" {
			role = "user"
		}
		parts = append(parts, messagePart{role: role, text: text})
	}
	if len(parts) == 0 {
		return "."
	}
	lastUserIndex := -1
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i].role == "user" {
			lastUserIndex = i
			break
		}
	}
	output := make([]string, 0, len(parts))
	for i, part := range parts {
		if i == lastUserIndex {
			output = append(output, part.text)
			continue
		}
		output = append(output, fmt.Sprintf("%s: %s", part.role, part.text))
	}
	return strings.Join(output, "\n\n")
}

func (p *grokProvider) headers() map[string]string {
	return map[string]string{
		"Accept":           "*/*",
		"Accept-Encoding":  "gzip, deflate, br, zstd",
		"Accept-Language":  "zh-CN,zh;q=0.9,en;q=0.8",
		"Content-Type":     "application/json",
		"Cookie":           buildGrokCookieHeader(p.cookieToken),
		"Origin":           p.origin,
		"Priority":         "u=1, i",
		"Referer":          p.referer,
		"Sec-Fetch-Dest":   "empty",
		"Sec-Fetch-Mode":   "cors",
		"User-Agent":       p.userAgent,
		"X-Statsig-Id":     randomGrokHex(8),
		"X-XAI-Request-Id": randomGrokHex(16),
		"X-Requested-With": "XMLHttpRequest",
	}
}

func (p *grokProvider) consumeStream(ctx context.Context, body io.ReadCloser, output chan<- core.TextStreamEvent) {
	defer body.Close()
	defer close(output)

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	filter := &grokStreamFilter{}
	var lastMessage string
	tokenSeen := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(line), &payload); err != nil {
			continue
		}
		response := nestedMap(payload, "result", "response")
		if response == nil {
			continue
		}
		if token, _ := response["token"].(string); token != "" {
			tokenSeen = true
			filtered := filter.filter(token)
			filtered = stripGrokArtifacts(filtered)
			if filtered == "" {
				continue
			}
			if !emitGrokEvent(ctx, output, core.TextStreamEvent{Delta: filtered}) {
				return
			}
			continue
		}
		modelResponse, _ := response["modelResponse"].(map[string]interface{})
		if modelResponse == nil {
			continue
		}
		message, _ := modelResponse["message"].(string)
		if message == "" || tokenSeen {
			continue
		}
		filtered := stripGrokArtifacts(message)
		delta := normalizeGrokChunk(filtered, &lastMessage)
		if delta == "" {
			continue
		}
		if !emitGrokEvent(ctx, output, core.TextStreamEvent{Delta: delta}) {
			return
		}
	}
	if err := scanner.Err(); err != nil {
		emitGrokEvent(ctx, output, core.TextStreamEvent{Err: err})
	}
}

func emitGrokEvent(ctx context.Context, output chan<- core.TextStreamEvent, event core.TextStreamEvent) bool {
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

func (f *grokStreamFilter) filter(token string) string {
	if token == "" {
		return ""
	}
	const startTag = "<xai:tool_usage_card"
	const endTag = "</xai:tool_usage_card>"
	var output strings.Builder
	remaining := token
	for remaining != "" {
		if f.toolCardOpen {
			endIndex := strings.Index(remaining, endTag)
			if endIndex == -1 {
				f.buffer.WriteString(remaining)
				return output.String()
			}
			endPos := endIndex + len(endTag)
			f.buffer.WriteString(remaining[:endPos])
			summary := summarizeGrokToolCard(f.buffer.String())
			if summary != "" {
				output.WriteString(summary)
				if !strings.HasSuffix(summary, "\n") {
					output.WriteString("\n")
				}
			}
			f.buffer.Reset()
			f.toolCardOpen = false
			remaining = remaining[endPos:]
			continue
		}
		startIndex := strings.Index(remaining, startTag)
		if startIndex == -1 {
			output.WriteString(remaining)
			break
		}
		if startIndex > 0 {
			output.WriteString(remaining[:startIndex])
		}
		endIndex := strings.Index(remaining[startIndex:], endTag)
		if endIndex == -1 {
			f.toolCardOpen = true
			f.buffer.WriteString(remaining[startIndex:])
			break
		}
		endPos := startIndex + endIndex + len(endTag)
		summary := summarizeGrokToolCard(remaining[startIndex:endPos])
		if summary != "" {
			output.WriteString(summary)
			if !strings.HasSuffix(summary, "\n") {
				output.WriteString("\n")
			}
		}
		remaining = remaining[endPos:]
	}
	return output.String()
}

func summarizeGrokToolCard(raw string) string {
	name := ""
	if matches := grokToolNameRE.FindStringSubmatch(raw); len(matches) == 2 {
		name = strings.TrimSpace(grokCDATARE.ReplaceAllString(matches[1], "$1"))
	}
	args := ""
	if matches := grokToolArgsRE.FindStringSubmatch(raw); len(matches) == 2 {
		args = strings.TrimSpace(grokCDATARE.ReplaceAllString(matches[1], "$1"))
	}
	if name == "" && args == "" {
		return ""
	}
	if args == "" {
		return fmt.Sprintf("[%s]", name)
	}
	return fmt.Sprintf("[%s] %s", name, args)
}

func stripGrokArtifacts(text string) string {
	if text == "" {
		return ""
	}
	cleaned := grokToolUsageCardRE.ReplaceAllStringFunc(text, summarizeGrokToolCard)
	cleaned = grokRolloutRE.ReplaceAllString(cleaned, "")
	cleaned = grokSpecialTagRE.ReplaceAllString(cleaned, "")
	return cleaned
}

func extractGrokMessageText(content interface{}) string {
	if content == nil {
		return ""
	}
	if text := strings.TrimSpace(core.ContentText(content)); text != "" {
		return text
	}
	if raw, err := json.Marshal(content); err == nil {
		return string(raw)
	}
	return ""
}

func buildGrokCookieHeader(token string) string {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return ""
	}
	if strings.Contains(trimmed, ";") {
		return trimmed
	}
	trimmed = strings.TrimPrefix(trimmed, "sso=")
	return fmt.Sprintf("sso=%s; sso-rw=%s", trimmed, trimmed)
}

func randomGrokHex(byteLen int) string {
	buf := make([]byte, byteLen)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func mapGrokModel(model string) string {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return "grok-4"
	}
	return trimmed
}

func normalizeGrokChunk(chunk string, previous *string) string {
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
	*previous = chunk
	return chunk
}

func nestedMap(root map[string]interface{}, keys ...string) map[string]interface{} {
	current := root
	for _, key := range keys {
		next, ok := current[key].(map[string]interface{})
		if !ok {
			return nil
		}
		current = next
	}
	return current
}
