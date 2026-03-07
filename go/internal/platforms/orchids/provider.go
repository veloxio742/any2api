package orchids

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"any2api-go/internal/core"
)

const (
	orchidsClerkQuerySuffix = "?__clerk_api_version=2025-11-10&_clerk_js_version=5.117.0"
	orchidsTokenTTL         = 50 * time.Minute
	orchidsUserAgent        = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Orchids/0.0.57 Chrome/138.0.7204.251 Electron/37.10.3 Safari/537.36"
	orchidsSystemPreset     = "你是 AI 编程助手，通过代理服务与用户交互。仅依赖当前工具和历史上下文，保持回复简洁专业。"
)

type orchidsProvider struct {
	client        *http.Client
	requestConfig core.RequestConfig
	apiURL        string
	clerkURL      string
	clientCookie  string
	clientUAT     string
	sessionID     string
	projectID     string
	userID        string
	email         string
	agentMode     string

	mu               sync.Mutex
	resolvedAccount  *orchidsAccount
	cachedToken      string
	cachedTokenUntil time.Time
}

type orchidsAccount struct {
	clientCookie string
	clientUAT    string
	sessionID    string
	projectID    string
	userID       string
	email        string
}

type orchidsTokenResponse struct {
	JWT string `json:"jwt"`
}

type orchidsClientResponse struct {
	Response struct {
		LastActiveSessionID string `json:"last_active_session_id"`
		Sessions            []struct {
			User struct {
				ID             string `json:"id"`
				EmailAddresses []struct {
					EmailAddress string `json:"email_address"`
				} `json:"email_addresses"`
			} `json:"user"`
		} `json:"sessions"`
	} `json:"response"`
}

type orchidsAgentRequest struct {
	Prompt        string        `json:"prompt"`
	ChatHistory   []interface{} `json:"chatHistory"`
	ProjectID     string        `json:"projectId"`
	CurrentPage   interface{}   `json:"currentPage"`
	AgentMode     string        `json:"agentMode"`
	Mode          string        `json:"mode"`
	GitRepoURL    string        `json:"gitRepoUrl"`
	Email         string        `json:"email"`
	ChatSessionID int           `json:"chatSessionId"`
	UserID        string        `json:"userId"`
	APIVersion    int           `json:"apiVersion"`
	Model         string        `json:"model,omitempty"`
}

type orchidsSSEMessage struct {
	Type  string                 `json:"type"`
	Event map[string]interface{} `json:"event,omitempty"`
}

func NewProvider() core.Provider {
	return NewProviderWithConfig(core.OrchidsConfig{})
}

func NewProviderWithConfig(cfg core.OrchidsConfig) core.Provider {
	if cfg.APIURL == "" {
		cfg.APIURL = core.DefaultOrchidsAPIURL
	}
	if cfg.ClerkURL == "" {
		cfg.ClerkURL = core.DefaultOrchidsClerkURL
	}
	if cfg.ProjectID == "" {
		cfg.ProjectID = core.DefaultOrchidsProjectID
	}
	if cfg.AgentMode == "" {
		cfg.AgentMode = core.DefaultOrchidsAgentMode
	}
	if cfg.Request.Timeout <= 0 {
		cfg.Request.Timeout = time.Duration(core.DefaultCursorTimeoutSeconds) * time.Second
	}
	if cfg.Request.MaxInputLength <= 0 {
		cfg.Request.MaxInputLength = core.DefaultCursorMaxInputLength
	}
	return &orchidsProvider{
		client:        &http.Client{Timeout: cfg.Request.Timeout},
		requestConfig: cfg.Request,
		apiURL:        strings.TrimRight(strings.TrimSpace(cfg.APIURL), "/"),
		clerkURL:      strings.TrimRight(strings.TrimSpace(cfg.ClerkURL), "/"),
		clientCookie:  trimCookieValue(cfg.ClientCookie, "__client="),
		clientUAT:     trimCookieValue(cfg.ClientUAT, "__client_uat="),
		sessionID:     strings.TrimSpace(cfg.SessionID),
		projectID:     strings.TrimSpace(cfg.ProjectID),
		userID:        strings.TrimSpace(cfg.UserID),
		email:         strings.TrimSpace(cfg.Email),
		agentMode:     strings.TrimSpace(cfg.AgentMode),
	}
}

func (*orchidsProvider) ID() string { return "orchids" }

func (*orchidsProvider) Capabilities() core.ProviderCapabilities {
	return core.ProviderCapabilities{OpenAICompatible: true, AnthropicCompatible: true, Tools: true, MultiAccount: true}
}

func (*orchidsProvider) Models() []core.ModelInfo {
	return []core.ModelInfo{{Provider: "orchids", PublicModel: "claude-sonnet-4.5", UpstreamModel: "claude-sonnet-4-5", OwnedBy: "orchids"}}
}

func (p *orchidsProvider) BuildUpstreamPreview(req core.UnifiedRequest) map[string]interface{} {
	return map[string]interface{}{
		"url":             p.apiURL,
		"auth":            "clerk session cookie -> jwt bearer",
		"live_enabled":    true,
		"configured":      p.clientCookie != "",
		"clerk_url":       p.clerkURL,
		"mapped_model":    p.mapModel(req.Model),
		"message_count":   len(req.Messages),
		"prompt_strategy": "messages -> orchids markdown prompt",
	}
}

func (*orchidsProvider) GenerateReply(req core.UnifiedRequest) string {
	if req.Model == "" {
		return "[orchids provider] mapped request to Orchids agent flow"
	}
	return fmt.Sprintf("[orchids provider] mapped request to Orchids agent flow for model=%s", req.Model)
}

func (p *orchidsProvider) CompleteOpenAI(ctx context.Context, req core.UnifiedRequest) (string, error) {
	return core.CollectTextStream(ctx, p.mustStream(ctx, req))
}

func (p *orchidsProvider) StreamOpenAI(ctx context.Context, req core.UnifiedRequest) (<-chan core.TextStreamEvent, error) {
	return p.stream(ctx, req)
}

func (p *orchidsProvider) CompleteAnthropic(ctx context.Context, req core.UnifiedRequest) (string, error) {
	return core.CollectTextStream(ctx, p.mustStream(ctx, req))
}

func (p *orchidsProvider) StreamAnthropic(ctx context.Context, req core.UnifiedRequest) (<-chan core.TextStreamEvent, error) {
	return p.stream(ctx, req)
}

func (p *orchidsProvider) mustStream(ctx context.Context, req core.UnifiedRequest) <-chan core.TextStreamEvent {
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

func (p *orchidsProvider) stream(ctx context.Context, req core.UnifiedRequest) (<-chan core.TextStreamEvent, error) {
	resp, err := p.doRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	output := make(chan core.TextStreamEvent, 32)
	go p.consumeStream(ctx, resp.Body, output)
	return output, nil
}

func (p *orchidsProvider) doRequest(ctx context.Context, req core.UnifiedRequest) (*http.Response, error) {
	account, cacheable, err := p.resolveAccount(ctx, req)
	if err != nil {
		return nil, err
	}
	token, err := p.getToken(ctx, account, cacheable)
	if err != nil {
		return nil, err
	}
	payload := p.buildAgentRequest(req, account)
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal orchids payload: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.apiURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build orchids request: %w", err)
	}
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Orchids-Api-Version", "2")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("orchids upstream request failed: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		p.invalidateToken()
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("orchids upstream error: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return resp, nil
}

func (p *orchidsProvider) resolveAccount(ctx context.Context, req core.UnifiedRequest) (*orchidsAccount, bool, error) {
	if override := p.requestAccountOverride(req); override != nil {
		if override.clientCookie == "" {
			return nil, false, fmt.Errorf("orchids client cookie is not configured")
		}
		if override.clientUAT == "" {
			override.clientUAT = fmt.Sprintf("%d", time.Now().Unix())
		}
		if override.sessionID == "" || override.userID == "" || override.email == "" {
			resolved, err := p.fetchAccountInfo(ctx, override.clientCookie)
			if err != nil {
				return nil, false, err
			}
			if override.sessionID == "" {
				override.sessionID = resolved.sessionID
			}
			if override.userID == "" {
				override.userID = resolved.userID
			}
			if override.email == "" {
				override.email = resolved.email
			}
		}
		if override.sessionID == "" || override.userID == "" || override.email == "" {
			return nil, false, fmt.Errorf("orchids account identity is incomplete")
		}
		return override, false, nil
	}

	p.mu.Lock()
	if p.resolvedAccount != nil {
		account := *p.resolvedAccount
		p.mu.Unlock()
		return &account, true, nil
	}
	account := orchidsAccount{
		clientCookie: p.clientCookie,
		clientUAT:    p.clientUAT,
		sessionID:    p.sessionID,
		projectID:    p.projectID,
		userID:       p.userID,
		email:        p.email,
	}
	p.mu.Unlock()

	if account.clientCookie == "" {
		return nil, false, fmt.Errorf("orchids client cookie is not configured")
	}
	if account.clientUAT == "" {
		account.clientUAT = fmt.Sprintf("%d", time.Now().Unix())
	}
	if account.sessionID == "" || account.userID == "" || account.email == "" {
		resolved, err := p.fetchAccountInfo(ctx, account.clientCookie)
		if err != nil {
			return nil, false, err
		}
		if account.sessionID == "" {
			account.sessionID = resolved.sessionID
		}
		if account.userID == "" {
			account.userID = resolved.userID
		}
		if account.email == "" {
			account.email = resolved.email
		}
	}
	if account.sessionID == "" || account.userID == "" || account.email == "" {
		return nil, false, fmt.Errorf("orchids account identity is incomplete")
	}
	p.mu.Lock()
	p.resolvedAccount = &account
	p.mu.Unlock()
	return &account, true, nil
}

func (p *orchidsProvider) fetchAccountInfo(ctx context.Context, clientCookie string) (*orchidsAccount, error) {
	url := p.clerkURL + "/v1/client" + orchidsClerkQuerySuffix
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build orchids clerk client request: %w", err)
	}
	req.Header.Set("User-Agent", orchidsUserAgent)
	req.Header.Set("Accept-Language", "zh-CN")
	req.AddCookie(&http.Cookie{Name: "__client", Value: clientCookie})

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch orchids account info: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("orchids account info error: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload orchidsClientResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode orchids account info: %w", err)
	}
	if len(payload.Response.Sessions) == 0 || payload.Response.LastActiveSessionID == "" {
		return nil, fmt.Errorf("orchids account info missing active session")
	}
	session := payload.Response.Sessions[0]
	if len(session.User.EmailAddresses) == 0 || session.User.ID == "" {
		return nil, fmt.Errorf("orchids account info missing user identity")
	}
	return &orchidsAccount{
		sessionID: payload.Response.LastActiveSessionID,
		userID:    session.User.ID,
		email:     session.User.EmailAddresses[0].EmailAddress,
	}, nil
}

func (p *orchidsProvider) getToken(ctx context.Context, account *orchidsAccount, cacheable bool) (string, error) {
	if cacheable {
		p.mu.Lock()
		if p.cachedToken != "" && time.Now().Before(p.cachedTokenUntil) {
			token := p.cachedToken
			p.mu.Unlock()
			return token, nil
		}
		p.mu.Unlock()
	}

	url := fmt.Sprintf("%s/v1/client/sessions/%s/tokens%s", p.clerkURL, account.sessionID, orchidsClerkQuerySuffix)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader("organization_id="))
	if err != nil {
		return "", fmt.Errorf("build orchids token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", fmt.Sprintf("__client=%s; __client_uat=%s", account.clientCookie, account.clientUAT))

	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch orchids token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		p.invalidateToken()
		return "", fmt.Errorf("orchids token request failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload orchidsTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode orchids token response: %w", err)
	}
	if strings.TrimSpace(payload.JWT) == "" {
		return "", fmt.Errorf("orchids token response missing jwt")
	}
	if cacheable {
		p.mu.Lock()
		p.cachedToken = payload.JWT
		p.cachedTokenUntil = time.Now().Add(orchidsTokenTTL)
		p.mu.Unlock()
	}
	return payload.JWT, nil
}

func (p *orchidsProvider) requestAccountOverride(req core.UnifiedRequest) *orchidsAccount {
	if len(req.ProviderOptions) == 0 {
		return nil
	}
	clientCookie := trimCookieValue(req.ProviderOptions["orchids_client_cookie"], "__client=")
	if clientCookie == "" {
		return nil
	}
	projectID := strings.TrimSpace(req.ProviderOptions["orchids_project_id"])
	if projectID == "" {
		projectID = p.projectID
	}
	if projectID == "" {
		projectID = core.DefaultOrchidsProjectID
	}
	return &orchidsAccount{
		clientCookie: clientCookie,
		clientUAT:    trimCookieValue(req.ProviderOptions["orchids_client_uat"], "__client_uat="),
		sessionID:    strings.TrimSpace(req.ProviderOptions["orchids_session_id"]),
		projectID:    projectID,
		userID:       strings.TrimSpace(req.ProviderOptions["orchids_user_id"]),
		email:        strings.TrimSpace(req.ProviderOptions["orchids_email"]),
	}
}

func (p *orchidsProvider) invalidateToken() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cachedToken = ""
	p.cachedTokenUntil = time.Time{}
}

func (p *orchidsProvider) buildAgentRequest(req core.UnifiedRequest, account *orchidsAccount) orchidsAgentRequest {
	mappedModel := p.mapModel(req.Model)
	agentMode := strings.TrimSpace(req.ProviderOptions["orchids_agent_mode"])
	if agentMode == "" {
		agentMode = p.agentMode
	}
	if agentMode == "" {
		agentMode = mappedModel
	}
	return orchidsAgentRequest{
		Prompt:        buildPrompt(core.NormalizeMessages(req, p.requestConfig.SystemPromptInject, p.requestConfig.MaxInputLength)),
		ChatHistory:   []interface{}{},
		ProjectID:     account.projectID,
		CurrentPage:   map[string]interface{}{},
		AgentMode:     agentMode,
		Mode:          "agent",
		GitRepoURL:    "",
		Email:         account.email,
		ChatSessionID: rand.IntN(90000000) + 10000000,
		UserID:        account.userID,
		APIVersion:    2,
		Model:         mappedModel,
	}
}

func (p *orchidsProvider) consumeStream(ctx context.Context, body io.ReadCloser, output chan<- core.TextStreamEvent) {
	defer body.Close()
	defer close(output)

	reader := bufio.NewReader(body)
	var buffer strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				if buffer.Len() > 0 {
					p.consumeChunk(ctx, buffer.String(), output)
				}
				return
			}
			emitOrchidsEvent(ctx, output, core.TextStreamEvent{Err: err})
			return
		}
		buffer.WriteString(line)
		if line == "\n" {
			p.consumeChunk(ctx, buffer.String(), output)
			buffer.Reset()
		}
	}
}

func (p *orchidsProvider) consumeChunk(ctx context.Context, chunk string, output chan<- core.TextStreamEvent) {
	for _, line := range strings.Split(chunk, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if data == "" {
			continue
		}
		var msg orchidsSSEMessage
		if err := json.Unmarshal([]byte(data), &msg); err != nil {
			continue
		}
		if msg.Type != "model" || msg.Event == nil {
			continue
		}
		eventType, _ := msg.Event["type"].(string)
		if eventType != "text-delta" {
			continue
		}
		delta, _ := msg.Event["delta"].(string)
		if delta == "" {
			continue
		}
		if !emitOrchidsEvent(ctx, output, core.TextStreamEvent{Delta: delta}) {
			return
		}
	}
}

func (p *orchidsProvider) mapModel(model string) string {
	lower := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.Contains(lower, "opus"):
		return "claude-opus-4.5"
	case strings.Contains(lower, "haiku"):
		return "gemini-3-flash"
	case lower == "":
		return p.Models()[0].UpstreamModel
	default:
		return "claude-sonnet-4-5"
	}
}

func buildPrompt(messages []core.Message) string {
	var systems []string
	var dialogue []core.Message
	for _, msg := range messages {
		text := strings.TrimSpace(core.ContentText(msg.Content))
		if text == "" {
			continue
		}
		if strings.EqualFold(msg.Role, "system") {
			systems = append(systems, text)
			continue
		}
		dialogue = append(dialogue, core.Message{Role: strings.ToLower(strings.TrimSpace(msg.Role)), Content: text})
	}

	sections := []string{}
	if len(systems) > 0 {
		sections = append(sections, fmt.Sprintf("<client_system>\n%s\n</client_system>", strings.Join(systems, "\n\n")))
	}
	sections = append(sections, fmt.Sprintf("<proxy_instructions>\n%s\n</proxy_instructions>", orchidsSystemPreset))
	if history := formatHistory(dialogue); history != "" {
		sections = append(sections, fmt.Sprintf("<conversation_history>\n%s\n</conversation_history>", history))
	}
	currentRequest := "继续"
	if len(dialogue) > 0 && strings.EqualFold(dialogue[len(dialogue)-1].Role, "user") {
		if text := strings.TrimSpace(core.ContentText(dialogue[len(dialogue)-1].Content)); text != "" {
			currentRequest = text
		}
	}
	sections = append(sections, fmt.Sprintf("<user_request>\n%s\n</user_request>", currentRequest))
	return strings.Join(sections, "\n\n")
}

func formatHistory(messages []core.Message) string {
	history := messages
	if len(history) > 0 && strings.EqualFold(history[len(history)-1].Role, "user") {
		history = history[:len(history)-1]
	}
	parts := make([]string, 0, len(history))
	turnIndex := 1
	for _, msg := range history {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		if role != "user" && role != "assistant" {
			continue
		}
		text := strings.TrimSpace(core.ContentText(msg.Content))
		if text == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("<turn index=\"%d\" role=\"%s\">\n%s\n</turn>", turnIndex, role, text))
		turnIndex++
	}
	return strings.Join(parts, "\n\n")
}

func emitOrchidsEvent(ctx context.Context, output chan<- core.TextStreamEvent, event core.TextStreamEvent) bool {
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

func trimCookieValue(value string, prefix string) string {
	trimmed := strings.TrimSpace(value)
	return strings.TrimPrefix(trimmed, prefix)
}
