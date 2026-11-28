package journal

import (
	"encoding/json"

	"github.com/chuanmingliu/golive/internal/skillsmcp"
)

// Historical prepare records carry only status, renderer, and whole milliseconds.
// Leave missing identities and sub-millisecond durations absent.
type SkillPreparation struct {
	TS          float64          `json:"t_s"`
	Turn        string           `json:"turn,omitempty"`
	Response    string           `json:"response,omitempty"`
	Status      string           `json:"status"`
	Version     int              `json:"rendering_version"`
	MS          int              `json:"ms"`
	US          *int             `json:"us,omitempty"`
	MemoryStart *int             `json:"memory_start,omitempty"`
	MemoryEnd   *int             `json:"memory_end,omitempty"`
	Trace       *skillsmcp.Trace `json:"trace,omitempty"`
}

func (cb *callBuild) readSkill(l Line) {
	if l.Msg != "skills: prepare" {
		return
	}
	ms, ok := l.Int("ms")
	if !ok || ms < 0 {
		return
	}
	row := SkillPreparation{TS: cb.at(l.Time), Turn: l.Get("turn"), Response: l.Get("response"), Status: l.Get("status"), Version: l.N("rendering_version"), MS: ms}
	optionalInt := func(key string) *int {
		if value, ok := l.Int(key); ok {
			return &value
		}
		return nil
	}
	row.US = optionalInt("us")
	row.MemoryStart = optionalInt("memory_version_start")
	row.MemoryEnd = optionalInt("memory_version_end")
	if raw := l.Get("trace"); raw != "" {
		var trace skillsmcp.Trace
		if json.Unmarshal([]byte(raw), &trace) == nil && len(trace.Steps) > 0 {
			row.Trace = &trace
		}
	}
	cb.call.SkillTraces = append(cb.call.SkillTraces, row)
}
