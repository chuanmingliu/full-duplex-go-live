package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDraftPreservesExistingSettings(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.json")
	out := filepath.Join(dir, "out.json")
	if err := os.WriteFile(in, []byte(`{"default":"original","plans":{"original":{"coverages":["交强险"]}},"custom":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run("plans", in, out, false); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(out)
	var got map[string]any
	_ = json.Unmarshal(raw, &got)
	if got["default"] != "original" || got["custom"] != true || got["plans"].(map[string]any)["motor-full"] == nil {
		t.Fatal(string(raw))
	}
	if err := run("plans", in, out, false); err == nil {
		t.Fatal("overwrote existing output")
	}
	if err := os.WriteFile(in, []byte(`{"coverages":{"952":{"table":{"2000000":12345}}},"custom":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	out = filepath.Join(dir, "rating.json")
	if err := run("rating", in, out, false); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(out)
	_ = json.Unmarshal(raw, &got)
	table := got["coverages"].(map[string]any)["952"].(map[string]any)["table"].(map[string]any)
	if table["2000000"] != float64(12345) || table["4000000"] == nil {
		t.Fatal(string(raw))
	}
}
