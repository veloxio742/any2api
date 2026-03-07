package platforms

import "any2api-go/internal/core"

func DefaultRegistry(cfg core.AppConfig) *core.Registry {
	r := core.NewRegistry(cfg.DefaultProvider)
	r.Register(NewCursorProviderWithConfig(cfg.Cursor))
	r.Register(NewKiroProviderWithConfig(cfg.Kiro))
	r.Register(NewGrokProviderWithConfig(cfg.Grok))
	r.Register(NewOrchidsProviderWithConfig(cfg.Orchids))
	return r
}
