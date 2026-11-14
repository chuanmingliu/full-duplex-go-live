package journal

import "testing"

func TestSkillTraceLegacyAndDetailed(t *testing.T) {
	cb := callBuild{}
	for _, input := range []string{
		`time=2026-11-10T14:30:06.246+08:00 level=INFO msg="skills: prepare" status=loaded ms=2 rendering_version=1`,
		`time=2026-11-10T14:31:06.246+08:00 level=INFO msg="skills: prepare" status=no_match ms=0 us=408 turn=item_15 memory_version_start=0 memory_version_end=0 trace="{\"revision\":8,\"steps\":[{\"stage\":\"prompts/get\",\"us\":390,\"outcome\":\"ok\"}]}"`,
	} {
		line, ok := ParseLine(input)
		if !ok {
			t.Fatal("invalid fixture")
		}
		cb.readSkill(line)
	}
	rows := cb.call.SkillTraces
	if len(rows) != 2 || rows[0].Turn != "" || rows[0].Trace != nil || rows[0].MS != 2 || rows[0].US != nil {
		t.Fatal("legacy fields inferred")
	}
	r := rows[1]
	if r.Trace == nil || len(r.Trace.Steps) != 1 || r.Trace.Revision != 8 || r.Turn != "item_15" || r.US == nil || *r.US != 408 || r.MemoryStart == nil || *r.MemoryStart != 0 {
		t.Fatalf("detailed trace lost: %+v", r)
	}
}
