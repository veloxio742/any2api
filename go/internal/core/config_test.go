package core

import (
	"testing"
	"time"
)

func TestLoadAppConfigFromEnvSupportsLegacyCursorEnvNames(t *testing.T) {
	t.Setenv("PORT", "9001")
	t.Setenv("API_KEY", "legacy-key")
	t.Setenv("ADMIN_PASSWORD", "admin-secret")
	t.Setenv("DATA_DIR", "runtime-data")
	t.Setenv("NEWPLATFORM2API_DEFAULT_PROVIDER", "cursor")
	t.Setenv("TIMEOUT", "75")
	t.Setenv("MAX_INPUT_LENGTH", "321")
	t.Setenv("SYSTEM_PROMPT_INJECT", "legacy system")
	t.Setenv("SCRIPT_URL", "https://legacy/script.js")
	t.Setenv("USER_AGENT", "Legacy UA")
	t.Setenv("UNMASKED_VENDOR_WEBGL", "Legacy Vendor")
	t.Setenv("UNMASKED_RENDERER_WEBGL", "Legacy Renderer")

	cfg := LoadAppConfigFromEnv()
	if cfg.Port != 9001 {
		t.Fatalf("expected port 9001, got %d", cfg.Port)
	}
	if cfg.APIKey != "legacy-key" {
		t.Fatalf("expected api key from legacy env, got %q", cfg.APIKey)
	}
	if cfg.AdminPassword != "admin-secret" {
		t.Fatalf("expected admin password from legacy env, got %q", cfg.AdminPassword)
	}
	if cfg.DataDir != "runtime-data" {
		t.Fatalf("expected data dir from legacy env, got %q", cfg.DataDir)
	}
	if cfg.Cursor.ScriptURL != "https://legacy/script.js" {
		t.Fatalf("expected legacy script url, got %q", cfg.Cursor.ScriptURL)
	}
	if cfg.Cursor.UserAgent != "Legacy UA" {
		t.Fatalf("expected legacy user agent, got %q", cfg.Cursor.UserAgent)
	}
	if cfg.Cursor.Fingerprint.WebGLVendor != "Legacy Vendor" {
		t.Fatalf("expected legacy webgl vendor, got %q", cfg.Cursor.Fingerprint.WebGLVendor)
	}
	if cfg.Cursor.Fingerprint.WebGLRenderer != "Legacy Renderer" {
		t.Fatalf("expected legacy webgl renderer, got %q", cfg.Cursor.Fingerprint.WebGLRenderer)
	}
	if cfg.Cursor.Request.Timeout != 75*time.Second {
		t.Fatalf("expected timeout 75s, got %s", cfg.Cursor.Request.Timeout)
	}
	if cfg.Cursor.Request.MaxInputLength != 321 {
		t.Fatalf("expected max input length 321, got %d", cfg.Cursor.Request.MaxInputLength)
	}
	if cfg.Cursor.Request.SystemPromptInject != "legacy system" {
		t.Fatalf("expected injected system prompt, got %q", cfg.Cursor.Request.SystemPromptInject)
	}
}

func TestLoadAppConfigFromEnvLoadsKiroAndGrokConfig(t *testing.T) {
	t.Setenv("NEWPLATFORM2API_KIRO_ACCESS_TOKEN", "kiro-token")
	t.Setenv("NEWPLATFORM2API_KIRO_MACHINE_ID", "machine-123")
	t.Setenv("NEWPLATFORM2API_KIRO_PREFERRED_ENDPOINT", "amazonq")
	t.Setenv("NEWPLATFORM2API_KIRO_CODEWHISPERER_URL", "https://cw.example.com")
	t.Setenv("NEWPLATFORM2API_KIRO_AMAZONQ_URL", "https://q.example.com")
	t.Setenv("NEWPLATFORM2API_KIRO_TIMEOUT", "12")
	t.Setenv("NEWPLATFORM2API_KIRO_MAX_INPUT_LENGTH", "999")
	t.Setenv("NEWPLATFORM2API_KIRO_SYSTEM_PROMPT_INJECT", "kiro prompt")

	t.Setenv("NEWPLATFORM2API_GROK_API_URL", "https://grok.example.com")
	t.Setenv("NEWPLATFORM2API_GROK_COOKIE_TOKEN", "grok-cookie")
	t.Setenv("NEWPLATFORM2API_GROK_USER_AGENT", "Grok UA")
	t.Setenv("NEWPLATFORM2API_GROK_ORIGIN", "https://origin.example.com")
	t.Setenv("NEWPLATFORM2API_GROK_REFERER", "https://referer.example.com/")
	t.Setenv("NEWPLATFORM2API_GROK_TIMEOUT", "34")
	t.Setenv("NEWPLATFORM2API_GROK_MAX_INPUT_LENGTH", "888")
	t.Setenv("NEWPLATFORM2API_GROK_SYSTEM_PROMPT_INJECT", "grok prompt")

	cfg := LoadAppConfigFromEnv()
	if cfg.Kiro.AccessToken != "kiro-token" {
		t.Fatalf("expected kiro access token, got %q", cfg.Kiro.AccessToken)
	}
	if cfg.Kiro.MachineID != "machine-123" {
		t.Fatalf("expected kiro machine id, got %q", cfg.Kiro.MachineID)
	}
	if cfg.Kiro.PreferredEndpoint != "amazonq" {
		t.Fatalf("expected kiro preferred endpoint, got %q", cfg.Kiro.PreferredEndpoint)
	}
	if cfg.Kiro.CodeWhispererURL != "https://cw.example.com" {
		t.Fatalf("expected kiro codewhisperer url, got %q", cfg.Kiro.CodeWhispererURL)
	}
	if cfg.Kiro.AmazonQURL != "https://q.example.com" {
		t.Fatalf("expected kiro amazon q url, got %q", cfg.Kiro.AmazonQURL)
	}
	if cfg.Kiro.Request.Timeout != 12*time.Second {
		t.Fatalf("expected kiro timeout 12s, got %s", cfg.Kiro.Request.Timeout)
	}
	if cfg.Kiro.Request.MaxInputLength != 999 {
		t.Fatalf("expected kiro max input length 999, got %d", cfg.Kiro.Request.MaxInputLength)
	}
	if cfg.Kiro.Request.SystemPromptInject != "kiro prompt" {
		t.Fatalf("expected kiro injected prompt, got %q", cfg.Kiro.Request.SystemPromptInject)
	}

	if cfg.Grok.APIURL != "https://grok.example.com" {
		t.Fatalf("expected grok api url, got %q", cfg.Grok.APIURL)
	}
	if cfg.Grok.CookieToken != "grok-cookie" {
		t.Fatalf("expected grok cookie token, got %q", cfg.Grok.CookieToken)
	}
	if cfg.Grok.UserAgent != "Grok UA" {
		t.Fatalf("expected grok user agent, got %q", cfg.Grok.UserAgent)
	}
	if cfg.Grok.Origin != "https://origin.example.com" {
		t.Fatalf("expected grok origin, got %q", cfg.Grok.Origin)
	}
	if cfg.Grok.Referer != "https://referer.example.com/" {
		t.Fatalf("expected grok referer, got %q", cfg.Grok.Referer)
	}
	if cfg.Grok.Request.Timeout != 34*time.Second {
		t.Fatalf("expected grok timeout 34s, got %s", cfg.Grok.Request.Timeout)
	}
	if cfg.Grok.Request.MaxInputLength != 888 {
		t.Fatalf("expected grok max input length 888, got %d", cfg.Grok.Request.MaxInputLength)
	}
	if cfg.Grok.Request.SystemPromptInject != "grok prompt" {
		t.Fatalf("expected grok injected prompt, got %q", cfg.Grok.Request.SystemPromptInject)
	}
}
