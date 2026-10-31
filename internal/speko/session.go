package speko

import (
	"encoding/json"
	"errors"

	"github.com/gorilla/websocket"
)

// Only normalize the initial session. RawMessage preserves vendor fields that
// GoLive's local SessionConfig may not yet know about.
func prepareStart(kind int, data []byte) ([]byte, error) {
	var event map[string]json.RawMessage
	if kind != websocket.TextMessage || json.Unmarshal(data, &event) != nil || event == nil {
		return nil, errors.New("first frame must be a JSON session.start")
	}
	var typ string
	if json.Unmarshal(event["type"], &typ) != nil || typ != "session.start" {
		return nil, errors.New("first frame must be session.start")
	}
	var session map[string]json.RawMessage
	if json.Unmarshal(event["session"], &session) != nil || session == nil {
		return nil, errors.New("session.start requires a session object")
	}
	if raw, ok := session["model"]; ok {
		var model string
		if json.Unmarshal(raw, &model) != nil || model != Model {
			return nil, errors.New("Speko live sessions require model gpt-live-1")
		}
	} else {
		session["model"], _ = json.Marshal(Model)
	}
	if raw, ok := session["golive"]; ok {
		var overrides map[string]json.RawMessage
		if json.Unmarshal(raw, &overrides) != nil || len(overrides) > 0 {
			return nil, errors.New("local golive engine overrides are not supported by Speko")
		}
		delete(session, "golive") // Existing GoLive clients send an empty block.
	}
	event["session"], _ = json.Marshal(session)
	return json.Marshal(event)
}
