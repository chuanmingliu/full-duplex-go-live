package focus

// The wire between golive and a diarization service, over a WebSocket.
//
// The client sends binary messages: 16 kHz PCM16 mono, each prefixed by an
// 8-byte little-endian session time in ms of its first sample. The server
// sends JSON text messages:
//
//	{"type":"ready","lag_ms":320,"frame_ms":80,"model":"nemotron-3-diarization"}
//	{"type":"frames","start_ms":1200,"frame_ms":80,"p":[[0.9,0.1],[0.9,0.0],…]}
//	{"type":"error","message":"…"}
//
// "frames" carries consecutive frames from start_ms, one row per frame, one
// column per speaker in arrival order. The server may send the same frame
// again with a later message; the client keeps the latest.

import (
	"encoding/binary"
	"encoding/json"
)

// Ready is the server's first message.
type Ready struct {
	Type    string `json:"type"`
	LagMS   int    `json:"lag_ms"`
	FrameMS int    `json:"frame_ms"`
	Model   string `json:"model"`
}

// FramesMsg is a batch of labelled frames.
type FramesMsg struct {
	Type    string      `json:"type"`
	StartMS int64       `json:"start_ms"`
	FrameMS int         `json:"frame_ms"`
	P       [][]float32 `json:"p"`
	Message string      `json:"message,omitempty"`
	// For an "enrolled" reply: how much voice the enrolment had, and for a
	// profile, whether it was found.
	VoicedMS int    `json:"voiced_ms,omitempty"`
	Profile  string `json:"profile,omitempty"`
	Found    *bool  `json:"found,omitempty"`
	Saved    bool   `json:"saved,omitempty"`
	// For an enrolment by agreement: how many utterances were offered, how
	// many agree, whether one alone stands (tentative), whether this one
	// was rejected as disagreeing with a kept profile.
	Candidates int  `json:"candidates,omitempty"`
	Agreeing   int  `json:"agreeing,omitempty"`
	Tentative  bool `json:"tentative,omitempty"`
	Rejected   bool `json:"rejected,omitempty"`
}

// EnrolMsg tells a target-speaker service whose voice the caller's is: the
// audio between from_ms and to_ms of session time (edge/tsv_server.py). A
// diarizer ignores it.
type EnrolMsg struct {
	Type   string `json:"type"`
	FromMS int64  `json:"from_ms,omitempty"`
	ToMS   int64  `json:"to_ms,omitempty"`
	// SaveAs keeps the enrolment under this name for the next call;
	// Profile enrols from a kept one instead of a span.
	SaveAs  string `json:"save_as,omitempty"`
	Profile string `json:"profile,omitempty"`
}

// EncodeAudio frames a chunk for the wire.
func EncodeAudio(pcm []byte, atMS int64) []byte {
	out := make([]byte, 8+len(pcm))
	binary.LittleEndian.PutUint64(out, uint64(atMS))
	copy(out[8:], pcm)
	return out
}

// DecodeAudio reads a chunk from the wire.
func DecodeAudio(msg []byte) (pcm []byte, atMS int64, ok bool) {
	if len(msg) < 8 {
		return nil, 0, false
	}
	return msg[8:], int64(binary.LittleEndian.Uint64(msg)), true
}

// Expand turns a frames message into Frames.
func (m FramesMsg) Expand() []Frame {
	out := make([]Frame, 0, len(m.P))
	for i, p := range m.P {
		s := m.StartMS + int64(i*m.FrameMS)
		out = append(out, Frame{StartMS: s, EndMS: s + int64(m.FrameMS), P: p})
	}
	return out
}

func parse(data []byte) (ready *Ready, frames *FramesMsg, err error) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return nil, nil, err
	}
	switch head.Type {
	case "ready":
		var r Ready
		err = json.Unmarshal(data, &r)
		return &r, nil, err
	default:
		var f FramesMsg
		err = json.Unmarshal(data, &f)
		return nil, &f, err
	}
}
