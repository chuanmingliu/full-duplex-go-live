package duplex

import (
	"strings"

	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/skillsmcp"
)

// skillSlotTemplate is evaluated once by the host. original_prompt contains the
// existing systemPrompt result, including the saved instructions, host policy
// and current working memory. Values may contain their own literal template
// syntax, which must never be evaluated during this substitution.
const skillSlotTemplate = "{{ original_prompt }}\n\n<additional_skill_instructions>\n{{ skill_content }}\n</additional_skill_instructions>"

func fillSkillContentSlot(messages []provider.Message, guidance []skillsmcp.Guidance) bool {
	if len(messages) == 0 || messages[0].Role != provider.RoleSystem || len(guidance) == 0 {
		return false
	}
	parts := make([]string, 0, len(guidance))
	for _, guide := range guidance {
		parts = append(parts, guide.Text)
	}
	messages[0].Content = strings.NewReplacer(
		"{{ original_prompt }}", messages[0].Content,
		"{{ skill_content }}", strings.Join(parts, "\n\n"),
	).Replace(skillSlotTemplate)
	return true
}
