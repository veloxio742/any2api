package core

import (
	"path/filepath"
	"testing"
)

func TestRuntimeManagerPersistsAndAppliesProviderSelections(t *testing.T) {
	base := DefaultAppConfig()
	base.APIKey = "base-key"
	base.DefaultProvider = "cursor"
	base.AdminPassword = "secret"
	configPath := filepath.Join(t.TempDir(), "admin.json")

	store, err := NewRuntimeManager(configPath, base)
	if err != nil {
		t.Fatalf("new runtime manager: %v", err)
	}

	if _, err := store.UpdateSettings("runtime-key", "kiro", "updated-secret"); err != nil {
		t.Fatalf("update settings: %v", err)
	}
	if _, err := store.ReplaceKiroAccounts([]KiroAccount{{
		Name:              "Primary",
		AccessToken:       "kiro-token",
		MachineID:         "machine-1",
		PreferredEndpoint: "amazonq",
		Active:            true,
	}}); err != nil {
		t.Fatalf("replace kiro accounts: %v", err)
	}
	if _, err := store.ReplaceGrokTokens([]GrokToken{{Name: "Main", CookieToken: "cookie-1", Active: true}}); err != nil {
		t.Fatalf("replace grok tokens: %v", err)
	}

	cfg := store.CurrentAppConfig()
	if cfg.APIKey != "runtime-key" {
		t.Fatalf("expected runtime api key, got %q", cfg.APIKey)
	}
	if cfg.DefaultProvider != "kiro" {
		t.Fatalf("expected default provider kiro, got %q", cfg.DefaultProvider)
	}
	if cfg.Kiro.AccessToken != "kiro-token" || cfg.Kiro.MachineID != "machine-1" {
		t.Fatalf("expected selected kiro account to be applied, got %#v", cfg.Kiro)
	}
	if cfg.Grok.CookieToken != "cookie-1" {
		t.Fatalf("expected selected grok token to be applied, got %q", cfg.Grok.CookieToken)
	}
	if store.AdminPassword() != "updated-secret" {
		t.Fatalf("expected updated admin password, got %q", store.AdminPassword())
	}

	reloaded, err := NewRuntimeManager(configPath, base)
	if err != nil {
		t.Fatalf("reload runtime manager: %v", err)
	}
	reloadedCfg := reloaded.CurrentAppConfig()
	if reloadedCfg.Kiro.AccessToken != "kiro-token" || reloadedCfg.Grok.CookieToken != "cookie-1" {
		t.Fatalf("expected persisted provider settings after reload, got %#v / %#v", reloadedCfg.Kiro, reloadedCfg.Grok)
	}
}
