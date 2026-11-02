package duplex

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/chuanmingliu/golive/internal/config"
)

// This opt-in report compares the original phrase controller with the intended
// labels of synthetic Chinese audio fixtures. It is not ASR timing or a whole
// engine simulation: final scripted text is supplied directly to PhraseSet.
func TestChineseBackchannelFixtureBaseline(t *testing.T) {
	out := os.Getenv("GOLIVE_CHINESE_BC_EVAL_OUT")
	if out == "" {
		t.Skip("set GOLIVE_CHINESE_BC_EVAL_OUT for the fixture baseline")
	}
	cfg, err := config.Load("../../configs/telephony.json")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../services/backchannel-detector/chinese_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		ID       string `json:"id"`
		Text     string `json:"text"`
		Expected string `json:"expected"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no fixtures")
	}
	phrases := NewPhraseSet(cfg.Duplex.UserBackchannelPhrases)
	type row struct {
		ID            string `json:"id"`
		Expected      string `json:"intended_label"`
		LegacyMatches bool   `json:"legacy_phrase_match"`
	}
	rows := make([]row, 0, len(cases))
	for _, c := range cases {
		rows = append(rows, row{c.ID, c.Expected, phrases.Matches(c.Text)})
	}
	data, err := json.MarshalIndent(struct {
		Scope string `json:"scope"`
		Rows  []row  `json:"rows"`
	}{"scripted final text only; excludes ASR, identity gates and latency", rows}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}
