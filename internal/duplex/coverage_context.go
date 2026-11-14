package duplex

import (
	"regexp"
	"strings"

	"github.com/chuanmingliu/golive/internal/provider"
)

var coverageFollowup = regexp.MustCompile(`^(那|嗯|好的|好|请|你)?[，, ]*(还有(其他|别的)(的)?(吗)?|都有哪些|全部(呢|有哪些)?|还有呢|继续说|完整列出(来)?|全部列出(来)?)[。？！?! ]*$`)
var coverageTopic = regexp.MustCompile(`(三者|司机|乘客)医保(外用药)?|医保外用药(责任险)?(-三者|-司机|-乘客)?|精神损害险(-三者|-司机|-乘客)?|绝对免赔率(-车损|-三者|-司机|-乘客)?|假日三者翻倍险|第三者责任险|三者险|车损险|划痕险|司机责任险|乘客责任险|交强险|车船税`)

// Carry only a nearby coverage label into a short follow-up. Do not replay old
// pricing instructions, arbitrary history or a topic across an unrelated user turn.
func coverageSelectionText(messages []provider.Message, current string) string {
	if !coverageFollowup.MatchString(strings.TrimSpace(current)) {
		return current
	}
	skippedCurrent := false
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m.Role != provider.RoleUser && m.Role != provider.RoleAssistant {
			continue
		}
		if m.Role == provider.RoleUser && !skippedCurrent && m.Content == current {
			skippedCurrent = true
			continue
		}
		topic := coverageTopic.FindString(m.Content)
		if topic != "" {
			for _, scope := range []string{"三者", "司机", "乘客"} {
				if strings.HasPrefix(topic, scope+"医保") {
					topic = "医保外用药责任险-" + scope
					break
				}
			}
			return topic + "有哪些选项？" + current
		}
		if m.Role == provider.RoleUser {
			break
		}
	}
	return current
}
