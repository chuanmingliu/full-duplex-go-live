package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/config"
)

// An operator's prompt lives in the browser they dial from, so a call placed
// from anywhere else carries none of it. With somewhere to keep it, the server
// writes down what the last answered call was asked to be — instructions and
// opening line — so the call can be placed again from the machine itself.
func TestTheLastAnsweredCallIsKeptForRedialling(t *testing.T) {
	path := filepath.Join(t.TempDir(), "last-call.json")
	ts, _, _ := gatewayServer(t, func(c *config.Config) {
		c.Realtime.HoldForOpening = true
		c.LastCallFile = path
	})
	c := dialRealtime(t, ts, "/v1/realtime?call=out-aaaa-0001")
	sendRaw(t, c.conn, activeCallSessionUpdate)
	c.first("session.updated", 2*time.Second)
	if _, err := os.Stat(path); err == nil {
		t.Fatal("the call was written down before it was answered")
	}

	postJSON(t, ts.URL+"/v1/opening", `{"greeting":"您好，这里是前台。","warm":false,"callId":"out-aaaa-0001"}`)
	var got lastCall
	await(t, "the call to be written down", 2*time.Second, func() bool {
		data, err := os.ReadFile(path)
		return err == nil && json.Unmarshal(data, &got) == nil
	})
	if got.Instructions != "你是电话语音助手，每次只说一两句话。" {
		t.Errorf("instructions kept: %q; want the client's, as it sent them", got.Instructions)
	}
	if got.Greeting != "您好，这里是前台。" || got.Voice != "alloy" || got.Call != "out-aaaa-0001" {
		t.Errorf("kept %+v; want the opening line, the voice and the call", got)
	}
	if got.SHA != shortSHA(got.Instructions) || len(got.SHA) != 12 {
		t.Errorf("instructions_sha = %q", got.SHA)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("the file is mode %o; a prompt at rest is its owner's to read and nobody else's", perm)
	}

	// A call that brought no instructions of its own does not replace one
	// that did.
	bare := dialRealtime(t, ts, "/v1/realtime?call=out-bbbb-0002")
	sendRaw(t, bare.conn, `{"type":"session.update","session":{"type":"realtime","voice":"alloy","input_audio_format":"pcm16","output_audio_format":"pcm16"}}`)
	bare.first("session.updated", 2*time.Second)
	postJSON(t, ts.URL+"/v1/opening", `{"greeting":"喂。","warm":false,"callId":"out-bbbb-0002"}`)
	time.Sleep(200 * time.Millisecond)
	data, _ := os.ReadFile(path)
	var after lastCall
	_ = json.Unmarshal(data, &after)
	if after.Call != "out-aaaa-0001" {
		t.Errorf("a call with no instructions replaced the one kept: %+v", after)
	}
}

// Off unless asked for: instructions are not written to disk by default.
func TestNoCallIsKeptWithoutSomewhereToKeepIt(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	ts, _, _ := gatewayServer(t, func(c *config.Config) { c.Realtime.HoldForOpening = true })
	c := dialRealtime(t, ts, "/v1/realtime?call=out-aaaa-0001")
	sendRaw(t, c.conn, activeCallSessionUpdate)
	c.first("session.updated", 2*time.Second)
	postJSON(t, ts.URL+"/v1/opening", `{"greeting":"您好。","warm":false,"callId":"out-aaaa-0001"}`)
	time.Sleep(200 * time.Millisecond)
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("files were written with last_call_file unset: %v", entries)
	}
}

// A phone gateway cannot ask for a call to be stored, so the endpoint can be
// told to keep them all: both sides, as tracks on one timeline, for measuring
// the line from the audio afterwards.
func TestRealtimeCallsAreStoredWhenTheEndpointIsToldTo(t *testing.T) {
	dir := t.TempDir()
	ts, _, _ := gatewayServer(t, func(c *config.Config) {
		c.StoreDir = dir
		c.Realtime.StoreCalls = true
	})
	c := dialRealtime(t, ts, "/v1/realtime?call=out-aaaa-0001")
	sendRaw(t, c.conn, activeCallSessionUpdate)
	c.first("session.updated", 2*time.Second)
	c.turn(16000, 1400)
	await(t, "the assistant to answer", 5*time.Second, func() bool { return c.audioMS(16000) > 0 })

	sessions, _ := os.ReadDir(dir)
	if len(sessions) != 1 {
		t.Fatalf("%d stored sessions; want the call", len(sessions))
	}
	for _, name := range []string{"input.pcm", "output.pcm"} {
		var size int64
		await(t, name+" to hold audio", 3*time.Second, func() bool {
			info, err := os.Stat(filepath.Join(dir, sessions[0].Name(), name))
			if err != nil {
				return false
			}
			size = info.Size()
			return size > 0
		})
		_ = size
	}
}

// And not otherwise: a call is somebody's voice.
func TestRealtimeCallsAreNotStoredByDefault(t *testing.T) {
	dir := t.TempDir()
	ts, _, _ := gatewayServer(t, func(c *config.Config) { c.StoreDir = dir })
	c := dialRealtime(t, ts, "/v1/realtime?call=out-aaaa-0001")
	sendRaw(t, c.conn, activeCallSessionUpdate)
	c.first("session.updated", 2*time.Second)
	c.turn(16000, 1400)
	await(t, "the assistant to answer", 5*time.Second, func() bool { return c.audioMS(16000) > 0 })
	if sessions, _ := os.ReadDir(dir); len(sessions) != 0 {
		t.Errorf("a call was stored without being asked for: %d session(s)", len(sessions))
	}
}
