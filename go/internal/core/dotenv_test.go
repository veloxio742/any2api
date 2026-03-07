package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnvLoadsConfigWithoutOverridingProcessEnv(t *testing.T) {
	preserveEnv(t, "NEWPLATFORM2API_API_KEY")
	preserveEnv(t, "CLIENT_COOKIE")
	preserveEnv(t, "AGENT_MODE")

	t.Setenv("NEWPLATFORM2API_API_KEY", "env-key")
	envFile := filepath.Join(t.TempDir(), ".env")
	content := "NEWPLATFORM2API_API_KEY=file-key\nCLIENT_COOKIE=file-cookie\nexport AGENT_MODE='claude-opus-4.5'\n"
	if err := os.WriteFile(envFile, []byte(content), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	if err := LoadDotEnv(envFile); err != nil {
		t.Fatalf("LoadDotEnv() error = %v", err)
	}

	cfg := LoadAppConfigFromEnv()
	if cfg.APIKey != "env-key" {
		t.Fatalf("expected process env api key to win, got %q", cfg.APIKey)
	}
	if cfg.Orchids.ClientCookie != "file-cookie" {
		t.Fatalf("expected orchids client cookie from .env, got %q", cfg.Orchids.ClientCookie)
	}
	if cfg.Orchids.AgentMode != "claude-opus-4.5" {
		t.Fatalf("expected orchids agent mode from .env, got %q", cfg.Orchids.AgentMode)
	}
}

func TestLoadDotEnvIgnoresMissingFiles(t *testing.T) {
	if err := LoadDotEnv(filepath.Join(t.TempDir(), "missing.env")); err != nil {
		t.Fatalf("LoadDotEnv() unexpected error for missing file: %v", err)
	}
}

func preserveEnv(t *testing.T, key string) {
	t.Helper()
	value, exists := os.LookupEnv(key)
	t.Cleanup(func() {
		if exists {
			_ = os.Setenv(key, value)
			return
		}
		_ = os.Unsetenv(key)
	})
}
