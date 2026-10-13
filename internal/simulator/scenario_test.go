package simulator

import (
	"errors"
	"strings"
	"testing"
)

func sampleScenario() Scenario {
	return Scenario{SchemaVersion: 1, ID: "synthetic", Language: "zh-CN", Persona: "A synthetic customer", Facts: map[string]string{"day": "Thursday"}, Greeting: "Hello", AnswerDelayMS: 1500, MaxDurationMS: 120000, MaxRemoteTurns: 12, IdleTimeoutMS: 15000}
}
func TestScenarioStrict(t *testing.T) {
	for _, input := range []string{`{"schema_version":1,"schema_version":1}`, `{"unknown":1}`, `{"schema_version":2}`, strings.Repeat(" ", MaxScenarioBytes+1), "{\"id\":\"\xff\"}"} {
		if _, err := DecodeScenario(strings.NewReader(input)); !errors.Is(err, ErrInvalidScenario) {
			t.Fatalf("accepted invalid JSON: %v", err)
		}
	}
	s := sampleScenario()
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	s.Actions = []Action{{ID: "a", Trigger: "remote_speech_started", Occurrence: 1, Kind: "speak", Text: "Wait", MaxHoldMS: 5001}}
	if s.Validate() == nil {
		t.Fatal("unbounded hold")
	}
	s = sampleScenario()
	s.RetainAudio = true
	if s.Validate() == nil {
		t.Fatal("unsupported retention accepted")
	}
}
