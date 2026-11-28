package duplex

import (
	"regexp"
	"strings"

	"github.com/chuanmingliu/golive/internal/provider"
)

var quoteConfirmationFollowup = regexp.MustCompile(`^(有|没有|没有了|就这样|算吧|你算吧|您算吧|我等着|我等你|好的，我等你算完|就这三档|就这三道|其他不动|其他不变|其他保障不变)[。！! ]*$|没有其他.{0,8}(改|调)|就按.*(算|报价)|就比较|取消这次(调整|改动)|先不改|(就|还是).*(按|这样|这三种|这几个|这些).*(算|报价|对比|比较)|^(嗯|好|好的|可以|对)[，, ]*(就这样|按).*(吧|算|报价)`)

var coverageFollowup = regexp.MustCompile(`^(那|嗯|好的|好|请|你)?[，, ]*(还有(其他|别的)(的)?(吗)?|都有哪些|全部(呢|有哪些)?|还有呢|继续说|完整列出(来)?|全部列出(来)?)[。？！?! ]*$`)
var coverageOptionFollowup = regexp.MustCompile(`(除了.*万.*(还有|其他|还能)|还有(其他|别的).*(选项|档位)|还能.*(选|调高)|还有什么可以调整)`)
var coverageChangeFollowup = regexp.MustCompile(`(调整到|调成|改成|改为|改到|换成|提高到|降低到).*([一二三四五六七八九十百两0-9]+万|最高|最低)`)
var coveragePriceFollowup = regexp.MustCompile(`(比较|对比).*([一二三四五六七八九十百两0-9]+万|这几档|这几个档|这些档)|([一二三四五六七八九十百两0-9]+万|这几档|这三个).*?(比较|对比)`)

var coverageTopic = regexp.MustCompile(`(三者|司机|乘客)医保(外用药)?|医保外(用药)?(责任险)?(-三者|-司机|-乘客)?|精神损害险(-三者|-司机|-乘客)?|绝对免赔率(-车损|-三者|-司机|-乘客)?|假日三者翻倍险|第三者责任险|三者险|车损险|划痕险|司机责任险|乘客责任险|交强险|车船税|三者|车损|司机|乘客|座位`)

// Carry only a nearby coverage label into a short follow-up. Do not replay old
// pricing instructions, arbitrary history or a topic across an unrelated user turn.
func coverageSelectionText(messages []provider.Message, current string) string {
	if quoteConfirmationFollowup.MatchString(strings.TrimSpace(current)) {
		for _, message := range messages {
			if message.Role == provider.RoleSystem && strings.Contains(message.Content, "【待确认报价范围，尚未计算】") {
				// Route guidance for a pending revision. The model and revision
				// guard still decide whether the caller actually approved it.
				return "待确认报价。" + current
			}
		}
	}
	implicit := coverageTopic.FindString(current) == ""
	price := implicit && (coveragePriceFollowup.MatchString(current) || coverageChangeFollowup.MatchString(current))
	options := coverageFollowup.MatchString(strings.TrimSpace(current)) || coverageOptionFollowup.MatchString(current)
	if options && !implicit {
		topic := coverageTopic.FindString(current)
		switch topic {
		case "座位":
			topic = "座位险"
		case "司机", "乘客":
			topic += "责任险"
		}
		return topic + "有哪些选项？" + current
	}
	if !price && !options {
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
			if price {
				return topic + "。" + current
			}
			return topic + "有哪些选项？" + current
		}
		if m.Role == provider.RoleUser {
			break
		}
	}
	return current
}
