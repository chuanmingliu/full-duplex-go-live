package duplex

import "strings"

// The hyphen before 三者 is a coverage-label separator. Strip it only
// at synthesis so it cannot be read as 负 in a quotation.
func textForSynthesis(text string) string {
	return strings.ReplaceAll(text, "-三者", "三者")
}
