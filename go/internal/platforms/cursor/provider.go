package cursor

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
	"net/http/cookiejar"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"any2api-go/internal/core"
)

const (
	cursorScriptCacheTTL = time.Minute
	cursorMaxRetries     = 2
)

type cursorProvider struct {
	client          *http.Client
	requestConfig   core.RequestConfig
	apiURL          string
	scriptURL       string
	cookie          string
	xIsHuman        string
	userAgent       string
	referer         string
	webGLVendor     string
	webGLRenderer   string
	mainJS          string
	envJS           string
	scriptCache     string
	scriptCacheTime time.Time
	scriptMutex     sync.RWMutex
	headerGenerator *cursorHeaderGenerator
	jsRunner        func(string) (string, error)
	sleep           func(time.Duration)
}

type cursorRequest struct {
	Context  []interface{}   `json:"context"`
	Model    string          `json:"model"`
	ID       string          `json:"id"`
	Messages []cursorMessage `json:"messages"`
	Trigger  string          `json:"trigger"`
}

type cursorMessage struct {
	Role  string       `json:"role"`
	Parts []cursorPart `json:"parts"`
}

type cursorPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type cursorEventData struct {
	Type      string `json:"type"`
	Delta     string `json:"delta,omitempty"`
	ErrorText string `json:"errorText,omitempty"`
}

type cursorJSONResponse struct {
	Text    string `json:"text"`
	Content string `json:"content"`
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func NewProvider() core.Provider {
	return NewProviderWithConfig(core.CursorConfig{})
}

func NewProviderWithConfig(cfg core.CursorConfig) core.Provider {
	if cfg.APIURL == "" {
		cfg.APIURL = core.DefaultCursorAPIURL
	}
	if cfg.ScriptURL == "" {
		cfg.ScriptURL = core.DefaultCursorScriptURL
	}
	if cfg.Request.Timeout <= 0 {
		cfg.Request.Timeout = time.Duration(core.DefaultCursorTimeoutSeconds) * time.Second
	}
	if cfg.Request.MaxInputLength <= 0 {
		cfg.Request.MaxInputLength = core.DefaultCursorMaxInputLength
	}
	if cfg.Fingerprint.WebGLVendor == "" {
		cfg.Fingerprint.WebGLVendor = core.DefaultCursorWebGLVendor
	}
	if cfg.Fingerprint.WebGLRenderer == "" {
		cfg.Fingerprint.WebGLRenderer = core.DefaultCursorWebGLRenderer
	}
	jar, _ := cookiejar.New(nil)
	mainJS, envJS := loadCursorJSAssets()
	return &cursorProvider{
		client:          &http.Client{Timeout: cfg.Request.Timeout, Jar: jar},
		requestConfig:   cfg.Request,
		apiURL:          cfg.APIURL,
		scriptURL:       cfg.ScriptURL,
		cookie:          strings.TrimSpace(cfg.Cookie),
		xIsHuman:        strings.TrimSpace(cfg.XIsHuman),
		userAgent:       strings.TrimSpace(cfg.UserAgent),
		referer:         strings.TrimSpace(cfg.Referer),
		webGLVendor:     cfg.Fingerprint.WebGLVendor,
		webGLRenderer:   cfg.Fingerprint.WebGLRenderer,
		mainJS:          mainJS,
		envJS:           envJS,
		headerGenerator: newCursorHeaderGenerator(),
		jsRunner:        runNodeJS,
		sleep:           time.Sleep,
	}
}

func (*cursorProvider) ID() string { return "cursor" }

func (*cursorProvider) Capabilities() core.ProviderCapabilities {
	return core.ProviderCapabilities{OpenAICompatible: true, AnthropicCompatible: true, Tools: true}
}

func (*cursorProvider) Models() []core.ModelInfo {
	return []core.ModelInfo{{Provider: "cursor", PublicModel: "claude-sonnet-4.6", UpstreamModel: "anthropic/claude-sonnet-4.6", OwnedBy: "cursor"}}
}

func (p *cursorProvider) BuildUpstreamPreview(req core.UnifiedRequest) map[string]interface{} {
	payload := p.buildCursorRequest(req)
	return map[string]interface{}{
		"url":               p.apiURL,
		"script_url":        p.scriptURL,
		"auth":              "dynamic browser fingerprint + x-is-human + optional cookie",
		"live_enabled":      true,
		"cookie_configured": p.cookie != "",
		"payload":           map[string]interface{}{"model": payload.Model, "trigger": payload.Trigger, "message_count": len(payload.Messages)},
	}
}

func (*cursorProvider) GenerateReply(req core.UnifiedRequest) string {
	if req.Model == "" {
		return "[cursor provider] mapped request to Cursor web chat flow"
	}
	return fmt.Sprintf("[cursor provider] mapped request to Cursor web chat flow for model=%s", req.Model)
}

func (p *cursorProvider) CompleteOpenAI(ctx context.Context, req core.UnifiedRequest) (string, error) {
	if req.Stream {
		return core.CollectTextStream(ctx, p.mustStreamOpenAI(ctx, req))
	}

	resp, err := p.doOpenAIRequest(ctx, req)
	if err != nil {
		return "", err
	}
	return parseCursorResponse(resp)

}

func (p *cursorProvider) StreamOpenAI(ctx context.Context, req core.UnifiedRequest) (<-chan core.TextStreamEvent, error) {
	output := make(chan core.TextStreamEvent, 16)
	resp, err := p.doOpenAIRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "application/json") {
		text, err := parseCursorResponse(resp)
		if err != nil {
			return nil, err
		}
		go func() {
			defer close(output)
			select {
			case <-ctx.Done():
				output <- core.TextStreamEvent{Err: ctx.Err()}
			case output <- core.TextStreamEvent{Delta: text}:
			}
		}()
		return output, nil
	}

	go p.consumeCursorSSE(ctx, resp.Body, output)
	return output, nil
}

func (p *cursorProvider) CompleteAnthropic(ctx context.Context, req core.UnifiedRequest) (string, error) {
	return p.CompleteOpenAI(ctx, req)
}

func (p *cursorProvider) StreamAnthropic(ctx context.Context, req core.UnifiedRequest) (<-chan core.TextStreamEvent, error) {
	return p.StreamOpenAI(ctx, req)
}

func (p *cursorProvider) doOpenAIRequest(ctx context.Context, req core.UnifiedRequest) (*http.Response, error) {
	payload := p.buildCursorRequest(req)
	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal cursor payload: %w", err)
	}

	var lastErr error
	for attempt := 1; attempt <= cursorMaxRetries; attempt++ {
		xIsHuman, err := p.resolveXIsHuman(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = fmt.Errorf("resolve x-is-human token: %w", err)
			if attempt < cursorMaxRetries {
				p.sleep(time.Second * time.Duration(attempt))
				continue
			}
			return nil, lastErr
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.apiURL, bytes.NewReader(jsonPayload))
		if err != nil {
			return nil, fmt.Errorf("build cursor request: %w", err)
		}
		for key, value := range p.chatHeaders(xIsHuman) {
			httpReq.Header.Set(key, value)
		}

		resp, err := p.client.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("cursor upstream request failed: %w", err)
			if attempt < cursorMaxRetries {
				p.sleep(time.Second * time.Duration(attempt))
				continue
			}
			return nil, lastErr
		}

		if resp.StatusCode == http.StatusForbidden && attempt < cursorMaxRetries {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			p.refreshFingerprint()
			p.clearScriptCache()
			p.sleep(time.Second * time.Duration(attempt))
			continue
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			defer resp.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			message := strings.TrimSpace(string(body))
			if message == "" {
				message = "empty upstream error body"
			}
			if strings.Contains(message, "Attention Required! | Cloudflare") {
				message = "Cloudflare 403"
			}
			return nil, fmt.Errorf("cursor upstream status %d: %s", resp.StatusCode, message)
		}
		return resp, nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("cursor upstream failed after %d attempts", cursorMaxRetries)
	}
	return nil, lastErr
}

func (p *cursorProvider) buildCursorRequest(req core.UnifiedRequest) cursorRequest {
	preparedMessages := core.NormalizeMessages(req, p.requestConfig.SystemPromptInject, p.requestConfig.MaxInputLength)
	return cursorRequest{
		Context:  []interface{}{},
		Model:    p.mapModel(req.Model),
		ID:       randomHex(16),
		Messages: toCursorMessages(preparedMessages),
		Trigger:  "submit-message",
	}
}

func (p *cursorProvider) mapModel(model string) string {
	for _, info := range p.Models() {
		if info.PublicModel == model {
			return info.UpstreamModel
		}
	}
	if model == "" {
		return p.Models()[0].UpstreamModel
	}
	return model
}

func (p *cursorProvider) chatHeaders(xIsHuman string) map[string]string {
	headers := p.headerGenerator.ChatHeaders(xIsHuman)
	headers["Accept"] = "text/event-stream, application/json"
	headers["Origin"] = "https://cursor.com"
	if p.cookie != "" {
		headers["Cookie"] = p.cookie
	}
	if p.userAgent != "" {
		headers["User-Agent"] = p.userAgent
	}
	if p.referer != "" {
		headers["Referer"] = p.referer
		headers["referer"] = p.referer
	}
	return headers
}

func (p *cursorProvider) scriptHeaders() map[string]string {
	headers := p.headerGenerator.ScriptHeaders()
	if p.userAgent != "" {
		headers["User-Agent"] = p.userAgent
	}
	if p.referer != "" {
		headers["Referer"] = p.referer
		headers["referer"] = p.referer
	}
	return headers
}

func (p *cursorProvider) resolveXIsHuman(ctx context.Context) (string, error) {
	if p.xIsHuman != "" {
		return p.xIsHuman, nil
	}
	return p.fetchXIsHuman(ctx)
}

func (p *cursorProvider) fetchXIsHuman(ctx context.Context) (string, error) {
	cached, lastFetch := p.cachedScript()

	var scriptBody string
	if cached != "" && time.Since(lastFetch) < cursorScriptCacheTTL {
		scriptBody = cached
	} else {
		resp, err := p.fetchCursorScript(ctx)
		if err != nil {
			if cached != "" {
				scriptBody = cached
			} else {
				p.clearScriptCache()
				return randomHex(64), nil
			}
		} else {
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				if cached != "" {
					scriptBody = cached
				} else {
					p.clearScriptCache()
					return randomHex(64), nil
				}
			} else {
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					if cached != "" {
						scriptBody = cached
					} else {
						p.clearScriptCache()
						return randomHex(64), nil
					}
				} else {
					scriptBody = string(body)
					p.storeScriptCache(scriptBody)
				}
			}
		}
	}

	compiled := p.prepareJS(scriptBody)
	value, err := p.jsRunner(compiled)
	if err != nil {
		p.clearScriptCache()
		return randomHex(64), nil
	}
	value = normalizeCursorJSResult(value)
	if value == "" {
		p.clearScriptCache()
		return randomHex(64), nil
	}
	return value, nil
}

func (p *cursorProvider) fetchCursorScript(ctx context.Context) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.scriptURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build cursor script request: %w", err)
	}
	for key, value := range p.scriptHeaders() {
		req.Header.Set(key, value)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch cursor script: %w", err)
	}
	return resp, nil
}

func (p *cursorProvider) prepareJS(cursorJS string) string {
	profile := p.headerGenerator.Profile()
	userAgent := firstNonEmpty(p.userAgent, profile.UserAgent)
	replacer := strings.NewReplacer(
		"$$currentScriptSrc$$", p.scriptURL,
		"$$UNMASKED_VENDOR_WEBGL$$", firstNonEmpty(p.webGLVendor, core.DefaultCursorWebGLVendor),
		"$$UNMASKED_RENDERER_WEBGL$$", firstNonEmpty(p.webGLRenderer, core.DefaultCursorWebGLRenderer),
		"$$userAgent$$", userAgent,
	)
	mainScript := replacer.Replace(firstNonEmpty(p.mainJS, fallbackCursorMainJS))
	mainScript = strings.Replace(mainScript, "$$env_jscode$$", firstNonEmpty(p.envJS, fallbackCursorEnvJS), 1)
	mainScript = strings.Replace(mainScript, "$$cursor_jscode$$", cursorJS, 1)
	return mainScript
}

func (p *cursorProvider) cachedScript() (string, time.Time) {
	p.scriptMutex.RLock()
	defer p.scriptMutex.RUnlock()
	return p.scriptCache, p.scriptCacheTime
}

func (p *cursorProvider) storeScriptCache(script string) {
	p.scriptMutex.Lock()
	defer p.scriptMutex.Unlock()
	p.scriptCache = script
	p.scriptCacheTime = time.Now()
}

func (p *cursorProvider) clearScriptCache() {
	p.scriptMutex.Lock()
	defer p.scriptMutex.Unlock()
	p.scriptCache = ""
	p.scriptCacheTime = time.Time{}
}

func (p *cursorProvider) refreshFingerprint() {
	p.headerGenerator.Refresh()
}

func parseCursorResponse(resp *http.Response) (string, error) {
	defer resp.Body.Close()
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(contentType, "application/json") {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", fmt.Errorf("read cursor json response: %w", err)
		}
		return extractCursorJSONText(body)
	}
	return extractCursorSSEText(resp.Body)
}

func extractCursorJSONText(body []byte) (string, error) {
	var payload cursorJSONResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("decode cursor json response: %w", err)
	}
	switch {
	case payload.Text != "":
		return payload.Text, nil
	case payload.Content != "":
		return payload.Content, nil
	case payload.Message.Content != "":
		return payload.Message.Content, nil
	case len(payload.Choices) > 0 && payload.Choices[0].Message.Content != "":
		return payload.Choices[0].Message.Content, nil
	default:
		return "", fmt.Errorf("cursor json response did not contain assistant content")
	}
}

func extractCursorSSEText(body io.Reader) (string, error) {
	var output strings.Builder
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			break
		}

		var event cursorEventData
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		switch event.Type {
		case "error":
			if event.ErrorText != "" {
				return "", fmt.Errorf("cursor upstream error: %s", event.ErrorText)
			}
		case "finish":
			return output.String(), nil
		default:
			if event.Delta != "" {
				output.WriteString(event.Delta)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read cursor sse response: %w", err)
	}
	if output.Len() == 0 {
		return "", fmt.Errorf("cursor upstream returned no assistant content")
	}
	return output.String(), nil
}

func (p *cursorProvider) consumeCursorSSE(ctx context.Context, body io.ReadCloser, output chan<- core.TextStreamEvent) {
	defer close(output)
	defer body.Close()
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			output <- core.TextStreamEvent{Err: ctx.Err()}
			return
		default:
		}

		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}

		var event cursorEventData
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		switch event.Type {
		case "error":
			output <- core.TextStreamEvent{Err: fmt.Errorf("cursor upstream error: %s", firstNonEmpty(event.ErrorText, "unknown error"))}
			return
		case "finish":
			return
		default:
			if event.Delta != "" {
				output <- core.TextStreamEvent{Delta: event.Delta}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		output <- core.TextStreamEvent{Err: fmt.Errorf("read cursor sse response: %w", err)}
	}
}

func (p *cursorProvider) mustStreamOpenAI(ctx context.Context, req core.UnifiedRequest) <-chan core.TextStreamEvent {
	events, err := p.StreamOpenAI(ctx, req)
	if err == nil {
		return events
	}
	output := make(chan core.TextStreamEvent, 1)
	output <- core.TextStreamEvent{Err: err}
	close(output)
	return output
}

func toCursorMessages(messages []core.Message) []cursorMessage {
	result := make([]cursorMessage, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == "" {
			continue
		}
		result = append(result, cursorMessage{
			Role: msg.Role,
			Parts: []cursorPart{{
				Type: "text",
				Text: core.ContentText(msg.Content),
			}},
		})
	}
	return result
}

func randomHex(length int) string {
	if length <= 0 {
		return ""
	}
	bytesLen := (length + 1) / 2
	b := make([]byte, bytesLen)
	if _, err := rand.Read(b); err != nil {
		fallback := fmt.Sprintf("%d", time.Now().UnixNano())
		if len(fallback) > length {
			return fallback[:length]
		}
		return fallback
	}
	encoded := hex.EncodeToString(b)
	if len(encoded) > length {
		return encoded[:length]
	}
	return encoded
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func loadCursorJSAssets() (string, string) {
	if mainJS, envJS, err := embeddedCursorJSAssets(); err == nil {
		return mainJS, envJS
	}
	if override := strings.TrimSpace(os.Getenv("NEWPLATFORM2API_CURSOR_JS_DIR")); override != "" {
		if mainJS, envJS, err := loadCursorJSAssetsFromDir(override); err == nil {
			return mainJS, envJS
		}
	}
	return fallbackCursorMainJS, fallbackCursorEnvJS
}

func runNodeJS(jsCode string) (string, error) {
	finalJS := `const crypto = require('crypto').webcrypto;
global.crypto = crypto;
globalThis.crypto = crypto;
if (typeof window === 'undefined') { global.window = global; }
window.crypto = crypto;
this.crypto = crypto;
` + jsCode
	cmd := exec.Command("node")
	cmd.Stdin = strings.NewReader(finalJS)
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("node.js execution failed (exit code: %d): %s", exitErr.ExitCode(), strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("failed to execute node.js: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

func normalizeCursorJSResult(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	var decoded string
	if err := json.Unmarshal([]byte(trimmed), &decoded); err == nil {
		return strings.TrimSpace(decoded)
	}
	return strings.Trim(trimmed, `"`)
}

const fallbackCursorMainJS = `
global.cursor_config = {
    currentScriptSrc: "$$currentScriptSrc$$",
    fp: {
        UNMASKED_VENDOR_WEBGL: "$$UNMASKED_VENDOR_WEBGL$$",
        UNMASKED_RENDERER_WEBGL: "$$UNMASKED_RENDERER_WEBGL$$",
        userAgent: "$$userAgent$$"
    }
}

$$env_jscode$$
$$cursor_jscode$$

Promise.resolve(window.V_C && window.V_C[0] ? window.V_C[0]() : "")
    .then(value => console.log(JSON.stringify(value)))
    .catch(error => {
        console.error(String(error));
        process.exit(1);
    });
`

const fallbackCursorEnvJS = `
window = global;
window.console = console;
window.document = { currentScript: { src: global.cursor_config.currentScriptSrc } };
window.navigator = { userAgent: global.cursor_config.fp.userAgent };
`
