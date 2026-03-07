package core

import (
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultServerPort           = 8099
	DefaultAPIKey               = "0000"
	DefaultAdminPassword        = "changeme"
	DefaultProviderID           = "cursor"
	DefaultDataDir              = "data"
	DefaultCursorAPIURL         = "https://cursor.com/api/chat"
	DefaultCursorScriptURL      = "https://cursor.com/_next/static/chunks/pages/_app.js"
	DefaultKiroCodeWhispererURL = "https://codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse"
	DefaultKiroAmazonQURL       = "https://q.us-east-1.amazonaws.com/generateAssistantResponse"
	DefaultGrokAPIURL           = "https://grok.com/rest/app-chat/conversations/new"
	DefaultGrokUserAgent        = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	DefaultGrokOrigin           = "https://grok.com"
	DefaultGrokReferer          = "https://grok.com/"
	DefaultOrchidsAPIURL        = "https://orchids-server.calmstone-6964e08a.westeurope.azurecontainerapps.io/agent/coding-agent"
	DefaultOrchidsClerkURL      = "https://clerk.orchids.app"
	DefaultOrchidsProjectID     = "280b7bae-cd29-41e4-a0a6-7f603c43b607"
	DefaultOrchidsAgentMode     = "claude-opus-4.5"
	DefaultCursorTimeoutSeconds = 60
	DefaultCursorMaxInputLength = 200000
	DefaultCursorWebGLVendor    = "Google Inc. (Intel)"
	DefaultCursorWebGLRenderer  = "ANGLE (Intel, Intel(R) UHD Graphics 620 Direct3D11 vs_5_0 ps_5_0, D3D11)"
)

type RequestConfig struct {
	Timeout            time.Duration
	MaxInputLength     int
	SystemPromptInject string
}

type BrowserFingerprintConfig struct {
	WebGLVendor   string
	WebGLRenderer string
}

type CursorConfig struct {
	APIURL      string
	ScriptURL   string
	Cookie      string
	XIsHuman    string
	UserAgent   string
	Referer     string
	Fingerprint BrowserFingerprintConfig
	Request     RequestConfig
}

type KiroConfig struct {
	AccessToken       string
	MachineID         string
	PreferredEndpoint string
	CodeWhispererURL  string
	AmazonQURL        string
	Request           RequestConfig
}

type GrokConfig struct {
	APIURL      string
	CookieToken string
	UserAgent   string
	Origin      string
	Referer     string
	Request     RequestConfig
}

type OrchidsConfig struct {
	APIURL       string
	ClerkURL     string
	ClientCookie string
	ClientUAT    string
	SessionID    string
	ProjectID    string
	UserID       string
	Email        string
	AgentMode    string
	Request      RequestConfig
}

type AppConfig struct {
	Port            int
	APIKey          string
	AdminPassword   string
	DataDir         string
	DefaultProvider string
	Cursor          CursorConfig
	Kiro            KiroConfig
	Grok            GrokConfig
	Orchids         OrchidsConfig
}

func DefaultAppConfig() AppConfig {
	return AppConfig{
		Port:            DefaultServerPort,
		APIKey:          DefaultAPIKey,
		AdminPassword:   DefaultAdminPassword,
		DataDir:         DefaultDataDir,
		DefaultProvider: DefaultProviderID,
		Cursor: CursorConfig{
			APIURL:    DefaultCursorAPIURL,
			ScriptURL: DefaultCursorScriptURL,
			Fingerprint: BrowserFingerprintConfig{
				WebGLVendor:   DefaultCursorWebGLVendor,
				WebGLRenderer: DefaultCursorWebGLRenderer,
			},
			Request: RequestConfig{
				Timeout:        time.Duration(DefaultCursorTimeoutSeconds) * time.Second,
				MaxInputLength: DefaultCursorMaxInputLength,
			},
		},
		Kiro: KiroConfig{
			CodeWhispererURL: DefaultKiroCodeWhispererURL,
			AmazonQURL:       DefaultKiroAmazonQURL,
			Request: RequestConfig{
				Timeout:        time.Duration(DefaultCursorTimeoutSeconds) * time.Second,
				MaxInputLength: DefaultCursorMaxInputLength,
			},
		},
		Grok: GrokConfig{
			APIURL:    DefaultGrokAPIURL,
			UserAgent: DefaultGrokUserAgent,
			Origin:    DefaultGrokOrigin,
			Referer:   DefaultGrokReferer,
			Request: RequestConfig{
				Timeout:        time.Duration(DefaultCursorTimeoutSeconds) * time.Second,
				MaxInputLength: DefaultCursorMaxInputLength,
			},
		},
		Orchids: OrchidsConfig{
			APIURL:    DefaultOrchidsAPIURL,
			ClerkURL:  DefaultOrchidsClerkURL,
			ProjectID: DefaultOrchidsProjectID,
			AgentMode: DefaultOrchidsAgentMode,
			Request: RequestConfig{
				Timeout:        time.Duration(DefaultCursorTimeoutSeconds) * time.Second,
				MaxInputLength: DefaultCursorMaxInputLength,
			},
		},
	}
}

func LoadAppConfigFromEnv() AppConfig {
	cfg := DefaultAppConfig()
	cfg.Port = envInt(cfg.Port, "NEWPLATFORM2API_PORT", "PORT")
	cfg.APIKey = envString(cfg.APIKey, "NEWPLATFORM2API_API_KEY", "API_KEY")
	cfg.AdminPassword = envString(cfg.AdminPassword, "NEWPLATFORM2API_ADMIN_PASSWORD", "ADMIN_PASSWORD")
	cfg.DataDir = envString(cfg.DataDir, "NEWPLATFORM2API_DATA_DIR", "DATA_DIR")
	cfg.DefaultProvider = envString(cfg.DefaultProvider, "NEWPLATFORM2API_DEFAULT_PROVIDER")

	cfg.Cursor.APIURL = envString(cfg.Cursor.APIURL, "NEWPLATFORM2API_CURSOR_API_URL")
	cfg.Cursor.ScriptURL = envString(cfg.Cursor.ScriptURL, "NEWPLATFORM2API_CURSOR_SCRIPT_URL", "SCRIPT_URL")
	cfg.Cursor.Cookie = envString(cfg.Cursor.Cookie, "NEWPLATFORM2API_CURSOR_COOKIE")
	cfg.Cursor.XIsHuman = envString(cfg.Cursor.XIsHuman, "NEWPLATFORM2API_CURSOR_X_IS_HUMAN")
	cfg.Cursor.UserAgent = envString(cfg.Cursor.UserAgent, "NEWPLATFORM2API_CURSOR_USER_AGENT", "USER_AGENT")
	cfg.Cursor.Referer = envString(cfg.Cursor.Referer, "NEWPLATFORM2API_CURSOR_REFERER")
	cfg.Cursor.Fingerprint.WebGLVendor = envString(cfg.Cursor.Fingerprint.WebGLVendor, "NEWPLATFORM2API_CURSOR_UNMASKED_VENDOR_WEBGL", "UNMASKED_VENDOR_WEBGL")
	cfg.Cursor.Fingerprint.WebGLRenderer = envString(cfg.Cursor.Fingerprint.WebGLRenderer, "NEWPLATFORM2API_CURSOR_UNMASKED_RENDERER_WEBGL", "UNMASKED_RENDERER_WEBGL")

	timeoutSeconds := envInt(int(cfg.Cursor.Request.Timeout/time.Second), "NEWPLATFORM2API_CURSOR_TIMEOUT", "TIMEOUT")
	if timeoutSeconds <= 0 {
		timeoutSeconds = DefaultCursorTimeoutSeconds
	}
	cfg.Cursor.Request.Timeout = time.Duration(timeoutSeconds) * time.Second

	maxInputLength := envInt(cfg.Cursor.Request.MaxInputLength, "NEWPLATFORM2API_CURSOR_MAX_INPUT_LENGTH", "MAX_INPUT_LENGTH")
	if maxInputLength <= 0 {
		maxInputLength = DefaultCursorMaxInputLength
	}
	cfg.Cursor.Request.MaxInputLength = maxInputLength
	cfg.Cursor.Request.SystemPromptInject = envString(cfg.Cursor.Request.SystemPromptInject, "NEWPLATFORM2API_CURSOR_SYSTEM_PROMPT_INJECT", "SYSTEM_PROMPT_INJECT")

	cfg.Kiro.AccessToken = envString(cfg.Kiro.AccessToken, "NEWPLATFORM2API_KIRO_ACCESS_TOKEN")
	cfg.Kiro.MachineID = envString(cfg.Kiro.MachineID, "NEWPLATFORM2API_KIRO_MACHINE_ID")
	cfg.Kiro.PreferredEndpoint = envString(cfg.Kiro.PreferredEndpoint, "NEWPLATFORM2API_KIRO_PREFERRED_ENDPOINT")
	cfg.Kiro.CodeWhispererURL = envString(cfg.Kiro.CodeWhispererURL, "NEWPLATFORM2API_KIRO_CODEWHISPERER_URL")
	cfg.Kiro.AmazonQURL = envString(cfg.Kiro.AmazonQURL, "NEWPLATFORM2API_KIRO_AMAZONQ_URL")

	kiroTimeoutSeconds := envInt(int(cfg.Kiro.Request.Timeout/time.Second), "NEWPLATFORM2API_KIRO_TIMEOUT")
	if kiroTimeoutSeconds <= 0 {
		kiroTimeoutSeconds = DefaultCursorTimeoutSeconds
	}
	cfg.Kiro.Request.Timeout = time.Duration(kiroTimeoutSeconds) * time.Second

	kiroMaxInputLength := envInt(cfg.Kiro.Request.MaxInputLength, "NEWPLATFORM2API_KIRO_MAX_INPUT_LENGTH")
	if kiroMaxInputLength <= 0 {
		kiroMaxInputLength = DefaultCursorMaxInputLength
	}
	cfg.Kiro.Request.MaxInputLength = kiroMaxInputLength
	cfg.Kiro.Request.SystemPromptInject = envString(cfg.Kiro.Request.SystemPromptInject, "NEWPLATFORM2API_KIRO_SYSTEM_PROMPT_INJECT")

	cfg.Grok.APIURL = envString(cfg.Grok.APIURL, "NEWPLATFORM2API_GROK_API_URL")
	cfg.Grok.CookieToken = envString(cfg.Grok.CookieToken, "NEWPLATFORM2API_GROK_COOKIE_TOKEN")
	cfg.Grok.UserAgent = envString(cfg.Grok.UserAgent, "NEWPLATFORM2API_GROK_USER_AGENT")
	cfg.Grok.Origin = envString(cfg.Grok.Origin, "NEWPLATFORM2API_GROK_ORIGIN")
	cfg.Grok.Referer = envString(cfg.Grok.Referer, "NEWPLATFORM2API_GROK_REFERER")

	grokTimeoutSeconds := envInt(int(cfg.Grok.Request.Timeout/time.Second), "NEWPLATFORM2API_GROK_TIMEOUT")
	if grokTimeoutSeconds <= 0 {
		grokTimeoutSeconds = DefaultCursorTimeoutSeconds
	}
	cfg.Grok.Request.Timeout = time.Duration(grokTimeoutSeconds) * time.Second

	grokMaxInputLength := envInt(cfg.Grok.Request.MaxInputLength, "NEWPLATFORM2API_GROK_MAX_INPUT_LENGTH")
	if grokMaxInputLength <= 0 {
		grokMaxInputLength = DefaultCursorMaxInputLength
	}
	cfg.Grok.Request.MaxInputLength = grokMaxInputLength
	cfg.Grok.Request.SystemPromptInject = envString(cfg.Grok.Request.SystemPromptInject, "NEWPLATFORM2API_GROK_SYSTEM_PROMPT_INJECT")

	cfg.Orchids.APIURL = envString(cfg.Orchids.APIURL, "NEWPLATFORM2API_ORCHIDS_API_URL")
	cfg.Orchids.ClerkURL = envString(cfg.Orchids.ClerkURL, "NEWPLATFORM2API_ORCHIDS_CLERK_URL")
	cfg.Orchids.ClientCookie = envString(cfg.Orchids.ClientCookie, "NEWPLATFORM2API_ORCHIDS_CLIENT_COOKIE", "CLIENT_COOKIE")
	cfg.Orchids.ClientUAT = envString(cfg.Orchids.ClientUAT, "NEWPLATFORM2API_ORCHIDS_CLIENT_UAT", "CLIENT_UAT")
	cfg.Orchids.SessionID = envString(cfg.Orchids.SessionID, "NEWPLATFORM2API_ORCHIDS_SESSION_ID", "SESSION_ID")
	cfg.Orchids.ProjectID = envString(cfg.Orchids.ProjectID, "NEWPLATFORM2API_ORCHIDS_PROJECT_ID", "PROJECT_ID")
	cfg.Orchids.UserID = envString(cfg.Orchids.UserID, "NEWPLATFORM2API_ORCHIDS_USER_ID", "USER_ID")
	cfg.Orchids.Email = envString(cfg.Orchids.Email, "NEWPLATFORM2API_ORCHIDS_EMAIL", "EMAIL")
	cfg.Orchids.AgentMode = envString(cfg.Orchids.AgentMode, "NEWPLATFORM2API_ORCHIDS_AGENT_MODE", "AGENT_MODE")

	orchidsTimeoutSeconds := envInt(int(cfg.Orchids.Request.Timeout/time.Second), "NEWPLATFORM2API_ORCHIDS_TIMEOUT")
	if orchidsTimeoutSeconds <= 0 {
		orchidsTimeoutSeconds = DefaultCursorTimeoutSeconds
	}
	cfg.Orchids.Request.Timeout = time.Duration(orchidsTimeoutSeconds) * time.Second

	orchidsMaxInputLength := envInt(cfg.Orchids.Request.MaxInputLength, "NEWPLATFORM2API_ORCHIDS_MAX_INPUT_LENGTH")
	if orchidsMaxInputLength <= 0 {
		orchidsMaxInputLength = DefaultCursorMaxInputLength
	}
	cfg.Orchids.Request.MaxInputLength = orchidsMaxInputLength
	cfg.Orchids.Request.SystemPromptInject = envString(cfg.Orchids.Request.SystemPromptInject, "NEWPLATFORM2API_ORCHIDS_SYSTEM_PROMPT_INJECT")

	return cfg
}

func envString(defaultValue string, keys ...string) string {
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			trimmed := strings.TrimSpace(value)
			if trimmed != "" {
				return trimmed
			}
		}
	}
	return defaultValue
}

func envInt(defaultValue int, keys ...string) int {
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			trimmed := strings.TrimSpace(value)
			if trimmed == "" {
				continue
			}
			parsed, err := strconv.Atoi(trimmed)
			if err == nil {
				return parsed
			}
		}
	}
	return defaultValue
}
