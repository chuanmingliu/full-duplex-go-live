package duplex

import "github.com/chuanmingliu/golive/internal/tools"

func (e *Engine) loadStreamingGuard() {
	base := e.opts.Cfg.ToolsBaseURL
	if base == "" || e.deps.LLM == nil || e.deps.TTS == nil {
		return
	}
	cfg, err := tools.FetchStreamingGuard(e.ctx, base, e.toolsToken())
	if err != nil {
		e.log.Debug("streaming guard: using built-in rules", "err", err)
		return
	}
	e.streamingGuard = cfg
	e.log.Info("streaming guard: configuration loaded", "version", cfg.Version, "enabled", cfg.Enabled, "rules", len(cfg.Rules))
}
