package live

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMessageContentAcceptsBothForms(t *testing.T) {
	var parts InputMessage
	if err := json.Unmarshal([]byte(
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"你好"},{"type":"input_text","text":"，在吗"}]}`,
	), &parts); err != nil {
		t.Fatal(err)
	}
	if got := parts.Content.Text(); got != "你好，在吗" {
		t.Errorf("parts joined to %q", got)
	}

	var plain InputMessage
	if err := json.Unmarshal([]byte(`{"role":"assistant","content":"我在"}`), &plain); err != nil {
		t.Fatal(err)
	}
	if got := plain.Content.Text(); got != "我在" {
		t.Errorf("a bare string decoded to %q", got)
	}

	// An image part is not text and must not turn into any.
	var mixed InputMessage
	if err := json.Unmarshal([]byte(
		`{"role":"user","content":[{"type":"input_image","image_url":"x"},{"type":"input_text","text":"看这个"}]}`,
	), &mixed); err != nil {
		t.Fatal(err)
	}
	if got := mixed.Content.Text(); got != "看这个" {
		t.Errorf("mixed content decoded to %q", got)
	}

	if err := json.Unmarshal([]byte(`{"role":"user","content":42}`), &plain); err == nil {
		t.Error("a number was accepted as message content")
	}
}

func TestFlexStringKeepsNonStringJSON(t *testing.T) {
	var item ResponseItem
	if err := json.Unmarshal([]byte(`{"type":"function_call_output","call_id":"c","output":"{\"ok\":true}"}`), &item); err != nil {
		t.Fatal(err)
	}
	if item.Output != `{"ok":true}` {
		t.Errorf("string output decoded to %q", item.Output)
	}
	if err := json.Unmarshal([]byte(`{"type":"function_call_output","call_id":"c","output":{"ok":true}}`), &item); err != nil {
		t.Fatal(err)
	}
	if item.Output != `{"ok":true}` {
		t.Errorf("object output decoded to %q", item.Output)
	}
}

func TestStampEventID(t *testing.T) {
	data, _ := json.Marshal(TranscriptDelta{
		Envelope: Envelope{Type: ServerInputTranscriptDelta},
		Delta:    "你好",
	})
	stamped := StampEventID(data, "event_7")

	var back struct {
		Type    string `json:"type"`
		EventID string `json:"event_id"`
		Delta   string `json:"delta"`
	}
	if err := json.Unmarshal(stamped, &back); err != nil {
		t.Fatalf("stamped event is not JSON: %v\n%s", err, stamped)
	}
	if back.EventID != "event_7" || back.Type != ServerInputTranscriptDelta || back.Delta != "你好" {
		t.Errorf("stamped event decoded as %+v", back)
	}

	// An event with no other fields, and one that is not ours, both survive.
	bare, _ := json.Marshal(Envelope{Type: "x"})
	if got := string(StampEventID(bare, "e")); got != `{"type":"x","event_id":"e"}` {
		t.Errorf("bare envelope stamped as %s", got)
	}
	odd := []byte(`{"delta":"a","type":"b"}`)
	if got := StampEventID(odd, "e"); string(got) != string(odd) {
		t.Errorf("an event not starting with type was rewritten: %s", got)
	}
	if got := StampEventID(stamped, "again"); string(got) != string(stamped) {
		t.Errorf("an event was stamped twice: %s", got)
	}
}

// The error's client_event_id is where gpt-live-1 puts it, and also where
// golive always had it.
func TestErrorNamesTheRejectedCommandInBothPlaces(t *testing.T) {
	data, _ := json.Marshal(NewError("invalid_request_error", "bad", "no", "cmd_1"))
	var back struct {
		ClientEventID string `json:"client_event_id"`
		Error         struct {
			ClientEventID string `json:"client_event_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.ClientEventID != "cmd_1" || back.Error.ClientEventID != "cmd_1" {
		t.Errorf("client_event_id missing from one of its two places: %s", data)
	}
}

func TestChatToolsConvertsTheResponsesForm(t *testing.T) {
	flat := json.RawMessage(`{"type":"function","name":"lookup","description":"查保单","parameters":{"type":"object","properties":{"id":{"type":"string"}}},"strict":true}`)
	nested := json.RawMessage(`{"type":"function","function":{"name":"book"}}`)
	hosted := json.RawMessage(`{"type":"web_search"}`)

	chat, dropped := ChatTools([]json.RawMessage{flat, nested, hosted})
	if len(chat) != 2 {
		t.Fatalf("converted %d tools, want 2", len(chat))
	}
	var got struct {
		Type     string `json:"type"`
		Function struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
			Strict      bool            `json:"strict"`
		} `json:"function"`
	}
	if err := json.Unmarshal(chat[0], &got); err != nil {
		t.Fatal(err)
	}
	if got.Type != "function" || got.Function.Name != "lookup" || got.Function.Description != "查保单" ||
		!got.Function.Strict || !strings.Contains(string(got.Function.Parameters), `"id"`) {
		t.Errorf("flat function converted to %s", chat[0])
	}
	if string(chat[1]) != string(nested) {
		t.Errorf("an already-nested function was rewritten: %s", chat[1])
	}
	if len(dropped) != 1 || dropped[0] != "web_search" {
		t.Errorf("dropped = %v, want [web_search]", dropped)
	}
}

func TestChatToolChoice(t *testing.T) {
	for _, s := range []string{"auto", "required", "none"} {
		if got := ChatToolChoice(s); got != s {
			t.Errorf("ChatToolChoice(%q) = %v", s, got)
		}
	}
	if got := ChatToolChoice("sometimes"); got != nil {
		t.Errorf("an unknown string was forwarded: %v", got)
	}
	var named any
	_ = json.Unmarshal([]byte(`{"type":"function","name":"lookup"}`), &named)
	out, _ := json.Marshal(ChatToolChoice(named))
	if string(out) != `{"function":{"name":"lookup"},"type":"function"}` {
		t.Errorf("named choice converted to %s", out)
	}
}

func TestResponsesDelegationMergeKeepsWhatIsOmitted(t *testing.T) {
	no, limit := false, 256
	base := ResponsesDelegation{
		Model:             "a",
		Instructions:      "keep",
		Tools:             []json.RawMessage{json.RawMessage(`{"type":"function","name":"x"}`)},
		ParallelToolCalls: &no,
		MaxOutputTokens:   &limit,
	}
	var update ResponsesDelegation
	_ = json.Unmarshal([]byte(`{"model":"b","tools":[]}`), &update)
	base.Merge(&update)

	if base.Model != "b" {
		t.Errorf("model = %q, want b", base.Model)
	}
	if base.Instructions != "keep" || base.ParallelToolCalls == nil || *base.ParallelToolCalls ||
		base.MaxOutputTokens == nil || *base.MaxOutputTokens != 256 {
		t.Errorf("omitted settings did not keep their values: %+v", base)
	}
	if base.Tools == nil || len(base.Tools) != 0 {
		t.Errorf("an explicit empty tool list should clear the tools; got %s", base.Tools)
	}
}
