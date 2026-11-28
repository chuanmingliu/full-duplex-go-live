package duplex

import (
	"strings"

	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/tools"
)

func (e *Engine) loadUserPrompt() {
	if e.opts.Cfg.ToolsBaseURL == "" || e.deps.LLM == nil {
		return
	}
	cfg, err := tools.FetchUserPrompt(e.ctx, e.opts.Cfg.ToolsBaseURL, e.toolsToken())
	if err != nil {
		e.log.Debug("user prompt: configuration unavailable; using the caller text", "err", err)
		return
	}
	e.userPrompt = cfg
	e.log.Info("user prompt: configuration loaded", "version", cfg.Version, "enabled", cfg.Enabled)
}

// Only outgoing completion messages are changed. History, tool data, ASR,
// quotation policies and skill selection continue to use the original input.
func (e *Engine) renderUserPrompt(messages []provider.Message, input tools.UserPromptInput, turnID string) {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := &messages[i]
		if msg.Role != provider.RoleUser {
			continue
		}
		if msg.Content != input.CallerText && !strings.HasSuffix(msg.Content, "\n"+input.CallerText) {
			return
		}
		guidance, err := e.userPrompt.Render(input)
		if err != nil {
			e.log.Warn("user prompt: render fallback", "turn", turnID, "version", e.userPrompt.Version)
			return
		}
		if guidance != "" {
			msg.Content += "\n\n[本轮配置的业务指引]\n" + guidance
			e.log.Info("user prompt: rendered", "turn", turnID, "version", e.userPrompt.Version, "bytes", len(guidance))
		}
		return
	}
}
