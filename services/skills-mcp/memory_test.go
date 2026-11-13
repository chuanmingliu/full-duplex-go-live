package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chuanmingliu/full-duplex-go-live/services/skills-mcp/internal/dreaming"
)

func memoryFixture(t *testing.T) (fixture, *memoryAPI) {
	t.Helper()
	s, err := openStore(filepath.Join(t.TempDir(), "catalog.json"), backend{})
	if err != nil {
		t.Fatal(err)
	}
	db, err := dreaming.Open(filepath.Join(t.TempDir(), "memory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	m := &memoryAPI{db: db, catalog: s, workerEnabled: true}
	server := httptest.NewServer(handler(s, "admin-private", "runtime-private", m))
	t.Cleanup(server.Close)
	return fixture{t, s, server.URL}, m
}

func adminMemory(t *testing.T, f fixture, method, path string, body any, want int) []byte {
	t.Helper()
	code, data := f.request(method, "/admin/memory"+path, "admin-private", body, nil)
	if code != want {
		t.Fatalf("%s %s: got %d want %d: %s", method, path, code, want, data)
	}
	return data
}

func TestDreamToPublishedMCPSkill(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(fmt.Sprintf("rendering-v%d", version), func(t *testing.T) {
			f, m := memoryFixture(t)
			c := f.s.catalog
			c.PromptRendering = &PromptRendering{Version: version}
			if _, err := f.s.publish(c); err != nil {
				t.Fatal(err)
			}
			e := dreaming.Episode{ID: "completed-call", Mode: "real", ClosedAt: time.Now().Add(-time.Hour), Evidence: []dreaming.Evidence{{ID: "user1", Kind: "user", Text: "Please repeat just the price"}, {ID: "agent1", Kind: "assistant", Text: "A long and unhelpful replay"}}, AudioRefs: []string{"/private/audio.wav"}}
			adminMemory(t, f, "POST", "/episodes", e, 200)
			t.Logf("CAPTURE: synthetic completed episode %s saved in local SQLite", e.ID)
			request := dreaming.DreamRequest{ID: "overnight-review", EpisodeIDs: []string{e.ID}}
			adminMemory(t, f, "POST", "/dreams", request, 409)
			adminMemory(t, f, "POST", "/feedback", dreaming.Feedback{EpisodeID: e.ID, ID: "feedback1", Text: "OWNER PRIVATE: repeat only the detail requested"}, 200)
			adminMemory(t, f, "POST", "/episodes/"+e.ID+"/release", obj{"owner_done": false}, 400)
			adminMemory(t, f, "POST", "/episodes/"+e.ID+"/release", obj{"owner_done": true}, 200)
			adminMemory(t, f, "POST", "/dreams", request, 200)
			t.Log("QUEUE: explicit release and owner-feedback fixture included in immutable review snapshot")
			p := dreaming.Proposal{Title: "Repeat only the requested detail", Observation: "The assistant replayed too much after a narrow request.", Lesson: "When asked to repeat just the price, repeat only the price from the current verified quote. If none is available, retrieve it using the normal quote workflow.", Evidence: []dreaming.Reference{{EpisodeID: e.ID, EvidenceID: "feedback1"}}, Match: []string{`(?i)repeat.*price`}, Cases: []dreaming.RoutingCase{{Utterance: "repeat just the price", Match: true}, {Utterance: "how does insurance work?", Match: false}}}
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var in struct {
					Messages []struct {
						Content string `json:"content"`
					} `json:"messages"`
				}
				if json.NewDecoder(r.Body).Decode(&in) != nil || len(in.Messages) != 2 || !strings.Contains(in.Messages[1].Content, "OWNER PRIVATE") {
					t.Error("review lost feedback")
				}
				content, _ := json.Marshal(obj{"lessons": []dreaming.Proposal{p}})
				json.NewEncoder(w).Encode(obj{"choices": []any{obj{"message": obj{"content": string(content)}, "finish_reason": "stop"}}})
			}))
			defer provider.Close()
			model, err := dreaming.NewModel(provider.URL, "test-reviewer", "test-secret")
			if err != nil {
				t.Fatal(err)
			}
			if err = m.db.Step(context.Background(), model, time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			var lessons []dreaming.Lesson
			if err = json.Unmarshal(adminMemory(t, f, "GET", "/lessons", nil, 200), &lessons); err != nil || len(lessons) != 1 {
				t.Fatal(err, lessons)
			}
			if len(f.result("skills/list", obj{})["skills"].([]any)) != 2 {
				t.Fatal("draft leaked into runtime")
			}
			t.Logf("DREAM: validated candidate %s is available for review; runtime catalog still has only its two seed skills", lessons[0].ID)
			path := "/lessons/" + lessons[0].ID + "/publish"
			adminMemory(t, f, "POST", path, obj{"revision": 2, "reviewed": false}, 400)
			adminMemory(t, f, "POST", path, obj{"revision": 1, "reviewed": true}, 409)
			adminMemory(t, f, "POST", path, obj{"revision": 2, "reviewed": true}, 200)
			adminMemory(t, f, "POST", path, obj{"revision": 2, "reviewed": true}, 200) // retry after lost response
			t.Log("PUBLISH: rejected unreviewed and stale-revision requests; simulated owner approval published revision 3; identical retry succeeded")
			if f.s.catalog.Revision != 3 {
				t.Fatal("idempotent publication bumped revision")
			}
			prompt := f.result("prompts/get", obj{"name": "outbound-turn", "arguments": obj{"event": "auto", "utterance": "repeat just the price", "state": "{}"}})
			messages := prompt["messages"].([]any)
			if len(messages) != 1 {
				t.Fatal(prompt)
			}
			uri := messages[0].(obj)["content"].(obj)["uri"].(string)
			if uri != "skill://"+lessons[0].ID+"/SKILL.md" {
				t.Fatal(uri)
			}
			entry := f.result("skills/get", obj{"uri": uri})["skill"].(obj)
			body := f.result("resources/read", obj{"uri": uri})["contents"].([]any)[0].(obj)["text"].(string)
			digest := entry["resources"].([]any)[0].(obj)["digest"]
			if digest != fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(body))) || !strings.Contains(body, p.Lesson) || strings.Contains(body, "OWNER PRIVATE") {
				t.Fatal("runtime content or integrity mismatch")
			}
			t.Logf("RECALL: prompts/get selected %s; skills/get and resources/read verified %s\n%s", uri, digest, body)
			meta := prompt["_meta"].(obj)[slotKey].(obj)
			if meta["prompt_rendering"].(obj)["version"] != float64(version) {
				t.Fatal(meta)
			}
			noMatch := f.result("prompts/get", obj{"name": "outbound-turn", "arguments": obj{"event": "auto", "utterance": "how does insurance work?"}})
			if len(noMatch["messages"].([]any)) != 0 {
				t.Fatal("unrelated turn recalled lesson")
			}
			t.Log("NO MATCH: unrelated utterance selected zero memories")
			// Disable using the existing catalog control; a publish retry cannot undo it.
			c = f.s.catalog
			c.Skills = append([]Skill(nil), c.Skills...)
			c.Skills[len(c.Skills)-1].Enabled = false
			if _, err = f.s.publish(c); err != nil {
				t.Fatal(err)
			}
			adminMemory(t, f, "POST", path, obj{"revision": 4, "reviewed": true}, 409)
			t.Log("DISABLE: catalog update revoked the skill; retrying publication returned conflict")
			if f.rpc("skills/get", obj{"uri": uri})["error"] == nil {
				t.Fatal("disabled memory remains exposed")
			}
		})
	}
}

func TestMemoryAuthorizationAndOptionalStartup(t *testing.T) {
	f, _ := memoryFixture(t)
	for _, path := range []string{"/status", "/lessons", "/dreams/one"} {
		code, _ := f.request("GET", "/admin/memory"+path, "runtime-private", nil, nil)
		if code != 401 {
			t.Fatal("runtime token read private memory", path, code)
		}
	}
	adminMemory(t, f, "POST", "/episodes", obj{"id": "x", "unknown": "field"}, 400)
	t.Setenv("GOLIVE_DREAM_DB", "")
	m, close, err := startMemory(f.s)
	if err != nil || m != nil {
		t.Fatal("memory enabled by default", err)
	}
	close()
	t.Setenv("GOLIVE_DREAM_DB", filepath.Join(t.TempDir(), "memory.sqlite"))
	t.Setenv("GOLIVE_DREAM_MODEL_URL", "")
	m, close, err = startMemory(f.s)
	if err != nil || m.workerEnabled {
		t.Fatal("capture-only configuration", err)
	}
	close()
}

func TestLiveMemorySettingsAndRedaction(t *testing.T) {
	f, m := memoryFixture(t)
	var live struct {
		Connected bool                  `json:"connected"`
		Settings  dreaming.Settings     `json:"settings"`
		Jobs      []dreaming.JobSummary `json:"jobs"`
	}
	data := adminMemory(t, f, "GET", "/live", nil, 200)
	if err := json.Unmarshal(data, &live); err != nil || !live.Connected || !live.Settings.Enabled || live.Settings.IdleSeconds != 30 {
		t.Fatal(string(data), err)
	}
	live.Settings.IdleSeconds = 7
	adminMemory(t, f, "PUT", "/settings", live.Settings, 200)
	adminMemory(t, f, "PUT", "/settings", live.Settings, 409)
	saved, err := m.db.Settings(context.Background())
	if err != nil || saved.IdleSeconds != 7 {
		t.Fatal(saved, err)
	}
	saved.IdleSeconds = -1
	adminMemory(t, f, "PUT", "/settings", saved, 400)
	e := dreaming.Episode{ID: "private-call", Mode: "simulation", ClosedAt: time.Now().Add(-time.Minute), Evidence: []dreaming.Evidence{{ID: "line", Kind: "user", Text: "PRIVATE TRANSCRIPT"}}}
	adminMemory(t, f, "POST", "/episodes", e, 200)
	adminMemory(t, f, "POST", "/dreams", dreaming.DreamRequest{ID: "private-job", EpisodeIDs: []string{e.ID}}, 200)
	data = adminMemory(t, f, "GET", "/live", nil, 200)
	if strings.Contains(string(data), "PRIVATE TRANSCRIPT") || !strings.Contains(string(data), "private-call") {
		t.Fatal("live status leaked source or lost provenance")
	}
	if code, _ := f.request("GET", "/admin/memory/live", "runtime-private", nil, nil); code != 401 {
		t.Fatal("runtime token read admin data", code)
	}
}
