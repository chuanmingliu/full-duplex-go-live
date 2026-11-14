package main

import "testing"

func TestExplicitStackProtocol(t *testing.T) {
	for _, kind := range []string{"golive", "realtime"} {
		s, err := parseStack("candidate=ws://127.0.0.1:18766/v1/realtime", kind)
		if err != nil || s.kind != kind {
			t.Fatalf("protocol %s: %v", kind, err)
		}
	}
	if _, err := parseStack("candidate=ws://localhost:1", "typo"); err == nil {
		t.Fatal("invalid protocol accepted")
	}
}
