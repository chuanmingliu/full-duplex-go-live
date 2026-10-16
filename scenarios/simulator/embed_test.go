package simulatorpresets

import "testing"

func TestEmbeddedPresetsAreValidIndependentCopies(t *testing.T) {
	presets, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(presets) != 5 {
		t.Fatal("missing presets")
	}
	seen := map[string]bool{}
	for _, preset := range presets {
		if seen[preset.ID] {
			t.Fatal("duplicate preset ID")
		}
		seen[preset.ID] = true
		if err := preset.Validate(); err != nil {
			t.Fatal(err)
		}
		if preset.RetainAudio {
			t.Fatal("presets must not retain audio")
		}
	}
	original := presets[0].Persona
	presets[0].Persona = "mutated"
	next, err := All()
	if err != nil || next[0].Persona != original {
		t.Fatal("caller mutated embedded preset")
	}
}
