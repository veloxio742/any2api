package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type AdminSettings struct {
	AdminPassword   string `json:"adminPassword,omitempty"`
	APIKey          string `json:"apiKey"`
	DefaultProvider string `json:"defaultProvider"`
}

type KiroAccount struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	AccessToken       string    `json:"accessToken"`
	MachineID         string    `json:"machineId"`
	PreferredEndpoint string    `json:"preferredEndpoint,omitempty"`
	Active            bool      `json:"active"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

type GrokToken struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	CookieToken string    `json:"cookieToken"`
	Active      bool      `json:"active"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type CursorRuntimeConfig struct {
	APIURL        string `json:"apiUrl"`
	ScriptURL     string `json:"scriptUrl"`
	Cookie        string `json:"cookie"`
	XIsHuman      string `json:"xIsHuman"`
	UserAgent     string `json:"userAgent"`
	Referer       string `json:"referer"`
	WebGLVendor   string `json:"webglVendor"`
	WebGLRenderer string `json:"webglRenderer"`
}

type OrchidsRuntimeConfig struct {
	APIURL       string `json:"apiUrl"`
	ClerkURL     string `json:"clerkUrl"`
	ClientCookie string `json:"clientCookie"`
	ClientUAT    string `json:"clientUat"`
	SessionID    string `json:"sessionId"`
	ProjectID    string `json:"projectId"`
	UserID       string `json:"userId"`
	Email        string `json:"email"`
	AgentMode    string `json:"agentMode"`
}

type ProviderState struct {
	Enabled        bool            `json:"enabled"`
	DisabledModels map[string]bool `json:"disabledModels,omitempty"`
}

type ProviderStore struct {
	CursorConfig   CursorRuntimeConfig      `json:"cursorConfig,omitempty"`
	KiroAccounts   []KiroAccount            `json:"kiroAccounts"`
	GrokTokens     []GrokToken              `json:"grokTokens"`
	OrchidsConfig  OrchidsRuntimeConfig     `json:"orchidsConfig,omitempty"`
	ProviderStates map[string]ProviderState `json:"providerStates,omitempty"`
}

type UserRecord struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"passwordHash"`
	Role         string    `json:"role"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type AdminConfig struct {
	Settings  AdminSettings `json:"settings"`
	Providers ProviderStore `json:"providers"`
	Users     []UserRecord  `json:"users,omitempty"`
}

type RuntimeManager struct {
	mu   sync.RWMutex
	path string
	base AppConfig
	data AdminConfig
}

func NewRuntimeManager(path string, base AppConfig) (*RuntimeManager, error) {
	mgr := &RuntimeManager{path: path, base: base, data: defaultAdminConfig(base)}
	if err := mgr.load(); err != nil {
		return nil, err
	}
	return mgr, nil
}

func (m *RuntimeManager) Snapshot() AdminConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneAdminConfig(m.data)
}

func (m *RuntimeManager) CurrentAppConfig() AppConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return applyAdminConfig(m.base, m.data)
}

func (m *RuntimeManager) AdminPassword() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return strings.TrimSpace(m.data.Settings.AdminPassword)
}

func (m *RuntimeManager) UpdateSettings(apiKey string, defaultProvider string, adminPassword string) (AdminConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data.Settings.APIKey = strings.TrimSpace(apiKey)
	defaultProvider = strings.TrimSpace(defaultProvider)
	if defaultProvider == "" {
		defaultProvider = m.base.DefaultProvider
	}
	m.data.Settings.DefaultProvider = defaultProvider
	if trimmed := strings.TrimSpace(adminPassword); trimmed != "" {
		m.data.Settings.AdminPassword = trimmed
	}
	if err := m.persistLocked(); err != nil {
		return AdminConfig{}, err
	}
	return cloneAdminConfig(m.data), nil
}

func (m *RuntimeManager) ReplaceKiroAccounts(accounts []KiroAccount) (AdminConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data.Providers.KiroAccounts = normalizeKiroAccounts(accounts)
	if err := m.persistLocked(); err != nil {
		return AdminConfig{}, err
	}
	return cloneAdminConfig(m.data), nil
}

func (m *RuntimeManager) ReplaceCursorConfig(cfg CursorRuntimeConfig) (AdminConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data.Providers.CursorConfig = normalizeCursorConfig(cfg)
	if err := m.persistLocked(); err != nil {
		return AdminConfig{}, err
	}
	return cloneAdminConfig(m.data), nil
}

func (m *RuntimeManager) ReplaceGrokTokens(tokens []GrokToken) (AdminConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data.Providers.GrokTokens = normalizeGrokTokens(tokens)
	if err := m.persistLocked(); err != nil {
		return AdminConfig{}, err
	}
	return cloneAdminConfig(m.data), nil
}

func (m *RuntimeManager) ReplaceOrchidsConfig(cfg OrchidsRuntimeConfig) (AdminConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data.Providers.OrchidsConfig = normalizeOrchidsConfig(cfg)
	if err := m.persistLocked(); err != nil {
		return AdminConfig{}, err
	}
	return cloneAdminConfig(m.data), nil
}

func (m *RuntimeManager) load() error {
	content, err := os.ReadFile(m.path)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("read admin config: %w", err)
		}
		return m.persistInitial()
	}
	if strings.TrimSpace(string(content)) == "" {
		return m.persistInitial()
	}
	loaded := defaultAdminConfig(m.base)
	if err := json.Unmarshal(content, &loaded); err != nil {
		return fmt.Errorf("decode admin config: %w", err)
	}
	if strings.TrimSpace(loaded.Settings.DefaultProvider) == "" {
		loaded.Settings.DefaultProvider = m.base.DefaultProvider
	}
	if strings.TrimSpace(loaded.Settings.AdminPassword) == "" {
		loaded.Settings.AdminPassword = m.base.AdminPassword
	}
	loaded.Providers.CursorConfig = normalizeCursorConfig(loaded.Providers.CursorConfig)
	loaded.Providers.KiroAccounts = normalizeKiroAccounts(loaded.Providers.KiroAccounts)
	loaded.Providers.GrokTokens = normalizeGrokTokens(loaded.Providers.GrokTokens)
	loaded.Providers.OrchidsConfig = normalizeOrchidsConfig(loaded.Providers.OrchidsConfig)
	m.data = loaded
	return nil
}

func (m *RuntimeManager) persistInitial() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data = defaultAdminConfig(m.base)
	return m.persistLocked()
}

func (m *RuntimeManager) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return fmt.Errorf("create admin config dir: %w", err)
	}
	data, err := json.MarshalIndent(m.data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal admin config: %w", err)
	}
	if err := os.WriteFile(m.path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write admin config: %w", err)
	}
	return nil
}

func defaultAdminConfig(base AppConfig) AdminConfig {
	return AdminConfig{
		Settings: AdminSettings{
			AdminPassword:   base.AdminPassword,
			APIKey:          base.APIKey,
			DefaultProvider: base.DefaultProvider,
		},
		Providers: ProviderStore{
			CursorConfig:  defaultCursorConfig(base),
			KiroAccounts:  normalizeKiroAccounts(defaultKiroAccounts(base)),
			GrokTokens:    normalizeGrokTokens(defaultGrokTokens(base)),
			OrchidsConfig: normalizeOrchidsConfig(defaultOrchidsConfig(base)),
		},
	}
}

func applyAdminConfig(base AppConfig, admin AdminConfig) AppConfig {
	cfg := base
	cfg.APIKey = admin.Settings.APIKey
	cfg.DefaultProvider = admin.Settings.DefaultProvider
	cfg.Cursor.APIURL = admin.Providers.CursorConfig.APIURL
	cfg.Cursor.ScriptURL = admin.Providers.CursorConfig.ScriptURL
	cfg.Cursor.Cookie = admin.Providers.CursorConfig.Cookie
	cfg.Cursor.XIsHuman = admin.Providers.CursorConfig.XIsHuman
	cfg.Cursor.UserAgent = admin.Providers.CursorConfig.UserAgent
	cfg.Cursor.Referer = admin.Providers.CursorConfig.Referer
	cfg.Cursor.Fingerprint.WebGLVendor = admin.Providers.CursorConfig.WebGLVendor
	cfg.Cursor.Fingerprint.WebGLRenderer = admin.Providers.CursorConfig.WebGLRenderer
	for _, account := range admin.Providers.KiroAccounts {
		if !account.Active {
			continue
		}
		cfg.Kiro.AccessToken = account.AccessToken
		cfg.Kiro.MachineID = account.MachineID
		cfg.Kiro.PreferredEndpoint = account.PreferredEndpoint
		break
	}
	for _, token := range admin.Providers.GrokTokens {
		if !token.Active {
			continue
		}
		cfg.Grok.CookieToken = token.CookieToken
		break
	}
	cfg.Orchids.APIURL = admin.Providers.OrchidsConfig.APIURL
	cfg.Orchids.ClerkURL = admin.Providers.OrchidsConfig.ClerkURL
	cfg.Orchids.ClientCookie = admin.Providers.OrchidsConfig.ClientCookie
	cfg.Orchids.ClientUAT = admin.Providers.OrchidsConfig.ClientUAT
	cfg.Orchids.SessionID = admin.Providers.OrchidsConfig.SessionID
	cfg.Orchids.ProjectID = admin.Providers.OrchidsConfig.ProjectID
	cfg.Orchids.UserID = admin.Providers.OrchidsConfig.UserID
	cfg.Orchids.Email = admin.Providers.OrchidsConfig.Email
	cfg.Orchids.AgentMode = admin.Providers.OrchidsConfig.AgentMode
	return cfg
}

func cloneAdminConfig(input AdminConfig) AdminConfig {
	clone := input
	clone.Providers.KiroAccounts = append([]KiroAccount(nil), input.Providers.KiroAccounts...)
	clone.Providers.GrokTokens = append([]GrokToken(nil), input.Providers.GrokTokens...)
	if input.Providers.ProviderStates != nil {
		clone.Providers.ProviderStates = make(map[string]ProviderState, len(input.Providers.ProviderStates))
		for k, v := range input.Providers.ProviderStates {
			state := v
			if v.DisabledModels != nil {
				state.DisabledModels = make(map[string]bool, len(v.DisabledModels))
				for mk, mv := range v.DisabledModels {
					state.DisabledModels[mk] = mv
				}
			}
			clone.Providers.ProviderStates[k] = state
		}
	}
	clone.Users = append([]UserRecord(nil), input.Users...)
	return clone
}

func defaultCursorConfig(base AppConfig) CursorRuntimeConfig {
	return normalizeCursorConfig(CursorRuntimeConfig{
		APIURL:        base.Cursor.APIURL,
		ScriptURL:     base.Cursor.ScriptURL,
		Cookie:        base.Cursor.Cookie,
		XIsHuman:      base.Cursor.XIsHuman,
		UserAgent:     base.Cursor.UserAgent,
		Referer:       base.Cursor.Referer,
		WebGLVendor:   base.Cursor.Fingerprint.WebGLVendor,
		WebGLRenderer: base.Cursor.Fingerprint.WebGLRenderer,
	})
}

func defaultKiroAccounts(base AppConfig) []KiroAccount {
	if strings.TrimSpace(base.Kiro.AccessToken) == "" && strings.TrimSpace(base.Kiro.MachineID) == "" {
		return nil
	}
	return []KiroAccount{{
		ID:                "kiro-env-default",
		Name:              "Env Default Kiro",
		AccessToken:       strings.TrimSpace(base.Kiro.AccessToken),
		MachineID:         strings.TrimSpace(base.Kiro.MachineID),
		PreferredEndpoint: strings.TrimSpace(strings.ToLower(base.Kiro.PreferredEndpoint)),
		Active:            true,
		UpdatedAt:         time.Now().UTC(),
	}}
}

func defaultGrokTokens(base AppConfig) []GrokToken {
	if strings.TrimSpace(base.Grok.CookieToken) == "" {
		return nil
	}
	return []GrokToken{{
		ID:          "grok-env-default",
		Name:        "Env Default Grok",
		CookieToken: strings.TrimSpace(base.Grok.CookieToken),
		Active:      true,
		UpdatedAt:   time.Now().UTC(),
	}}
}

func defaultOrchidsConfig(base AppConfig) OrchidsRuntimeConfig {
	return normalizeOrchidsConfig(OrchidsRuntimeConfig{
		APIURL:       base.Orchids.APIURL,
		ClerkURL:     base.Orchids.ClerkURL,
		ClientCookie: base.Orchids.ClientCookie,
		ClientUAT:    base.Orchids.ClientUAT,
		SessionID:    base.Orchids.SessionID,
		ProjectID:    base.Orchids.ProjectID,
		UserID:       base.Orchids.UserID,
		Email:        base.Orchids.Email,
		AgentMode:    base.Orchids.AgentMode,
	})
}

func normalizeCursorConfig(cfg CursorRuntimeConfig) CursorRuntimeConfig {
	cfg.APIURL = strings.TrimSpace(cfg.APIURL)
	cfg.ScriptURL = strings.TrimSpace(cfg.ScriptURL)
	cfg.Cookie = strings.TrimSpace(cfg.Cookie)
	cfg.XIsHuman = strings.TrimSpace(cfg.XIsHuman)
	cfg.UserAgent = strings.TrimSpace(cfg.UserAgent)
	cfg.Referer = strings.TrimSpace(cfg.Referer)
	cfg.WebGLVendor = strings.TrimSpace(cfg.WebGLVendor)
	cfg.WebGLRenderer = strings.TrimSpace(cfg.WebGLRenderer)
	return cfg
}

func normalizeKiroAccounts(accounts []KiroAccount) []KiroAccount {
	normalized := make([]KiroAccount, 0, len(accounts))
	activeSet := false
	now := time.Now().UTC()
	for idx, account := range accounts {
		account.AccessToken = strings.TrimSpace(account.AccessToken)
		account.MachineID = strings.TrimSpace(account.MachineID)
		account.PreferredEndpoint = strings.TrimSpace(strings.ToLower(account.PreferredEndpoint))
		if account.AccessToken == "" && account.MachineID == "" {
			continue
		}
		account.ID = normalizedID(account.ID, "kiro", idx)
		account.Name = strings.TrimSpace(account.Name)
		if account.Name == "" {
			account.Name = fmt.Sprintf("Kiro Account %d", len(normalized)+1)
		}
		if activeSet {
			account.Active = false
		} else if account.Active {
			activeSet = true
		}
		if account.UpdatedAt.IsZero() {
			account.UpdatedAt = now
		}
		normalized = append(normalized, account)
	}
	if !activeSet && len(normalized) > 0 {
		normalized[0].Active = true
	}
	return normalized
}

func normalizeGrokTokens(tokens []GrokToken) []GrokToken {
	normalized := make([]GrokToken, 0, len(tokens))
	activeSet := false
	now := time.Now().UTC()
	for idx, token := range tokens {
		token.CookieToken = strings.TrimSpace(token.CookieToken)
		if token.CookieToken == "" {
			continue
		}
		token.ID = normalizedID(token.ID, "grok", idx)
		token.Name = strings.TrimSpace(token.Name)
		if token.Name == "" {
			token.Name = fmt.Sprintf("Grok Token %d", len(normalized)+1)
		}
		if activeSet {
			token.Active = false
		} else if token.Active {
			activeSet = true
		}
		if token.UpdatedAt.IsZero() {
			token.UpdatedAt = now
		}
		normalized = append(normalized, token)
	}
	if !activeSet && len(normalized) > 0 {
		normalized[0].Active = true
	}
	return normalized
}

func normalizeOrchidsConfig(cfg OrchidsRuntimeConfig) OrchidsRuntimeConfig {
	cfg.APIURL = strings.TrimSpace(cfg.APIURL)
	cfg.ClerkURL = strings.TrimSpace(cfg.ClerkURL)
	cfg.ClientCookie = strings.TrimSpace(cfg.ClientCookie)
	cfg.ClientUAT = strings.TrimSpace(cfg.ClientUAT)
	cfg.SessionID = strings.TrimSpace(cfg.SessionID)
	cfg.ProjectID = strings.TrimSpace(cfg.ProjectID)
	cfg.UserID = strings.TrimSpace(cfg.UserID)
	cfg.Email = strings.TrimSpace(cfg.Email)
	cfg.AgentMode = strings.TrimSpace(cfg.AgentMode)
	return cfg
}

func normalizedID(value string, prefix string, idx int) string {
	trimmed := strings.TrimSpace(value)
	if trimmed != "" {
		return trimmed
	}
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), idx)
}
