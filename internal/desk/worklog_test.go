package desk

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The work log: Codex posts lines as it works, the owner tails them live;
// a long digit run is masked before it is stored.
func TestTheWorkLogIsAppendedAndTailedLive(t *testing.T) {
	dir := t.TempDir()
	s := openTest(t, dir)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	do := func(who, method, path, ctype, body string) (int, map[string]any) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+s.tokens[who])
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	// Codex opens a stream with where it runs, then two lines of a command.
	if code, out := do("codex", "POST", "/api/logs/local-talker", "text/plain", "host=mac device=mlx model=Qwen3-0.6B\n"); code != 200 || out["added"] != float64(1) {
		t.Fatalf("first line: %d %v", code, out)
	}
	if code, out := do("codex", "POST", "/api/logs/local-talker", "text/plain", "step 1/3 extracting 80 calls\n\ncalled 13912345678 — masked\n"); code != 200 || out["added"] != float64(2) || out["total"] != float64(3) {
		t.Fatalf("two lines: %d %v", code, out)
	}
	// No token: refused. A bad stream name: refused.
	if code, _ := do("", "POST", "/api/logs/local-talker", "text/plain", "x"); code != 401 {
		t.Fatalf("no token: %d", code)
	}
	// Depending on the standard library, path cleaning may reject directly,
	// redirect to a missing route, or preserve POST on a redirect (Go 1.26),
	// which the GET-only fallback rejects with 405. None may accept the write.
	if code, _ := do("codex", "POST", "/api/logs/../etc", "text/plain", "x"); code != 400 && code != 404 && code != 405 {
		t.Fatalf("a bad name: %d", code)
	}
	// The owner reads it: three lines, the number masked, the author kept.
	code, out := do("owner", "GET", "/api/logs/local-talker?after=1", "", "")
	lines, _ := out["lines"].([]any)
	if code != 200 || len(lines) != 2 || out["total"] != float64(3) {
		t.Fatalf("read: %d %v", code, out)
	}
	last := lines[1].(map[string]any)
	if last["who"] != "codex" || strings.Contains(last["text"].(string), "13912345678") || !strings.Contains(last["text"].(string), "<num>") {
		t.Fatalf("the digits were not masked: %v", last)
	}
	// The list says where it runs.
	_, out = do("owner", "GET", "/api/logs", "", "")
	streams, _ := out["streams"].([]any)
	if len(streams) != 1 || streams[0].(map[string]any)["first"] != "host=mac device=mlx model=Qwen3-0.6B" || streams[0].(map[string]any)["lines"] != float64(3) {
		t.Fatalf("list: %v", out)
	}
	// A live tail: the lines so far, then a new one as it is posted.
	req, _ := http.NewRequest("GET", srv.URL+"/api/logs/local-talker/tail?after=2", nil)
	req.Header.Set("Authorization", "Bearer "+s.tokens["owner"])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	r := bufio.NewReader(resp.Body)
	readEvent := func() string {
		for {
			line, err := r.ReadString('\n')
			if err != nil && err != io.EOF {
				t.Fatal(err)
			}
			if strings.HasPrefix(line, "data: ") {
				return strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			}
		}
	}
	var l WorkLine
	json.Unmarshal([]byte(readEvent()), &l)
	if l.N != 3 {
		t.Fatalf("the tail did not start after line 2: %+v", l)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		do("codex", "POST", "/api/logs/local-talker", "application/json", `{"lines":["step 2/3 training","loss 1.23"]}`)
	}()
	json.Unmarshal([]byte(readEvent()), &l)
	if l.N != 4 || l.Text != "step 2/3 training" {
		t.Fatalf("the new line did not arrive on the tail: %+v", l)
	}
}
