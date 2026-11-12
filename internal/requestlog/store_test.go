package requestlog

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPersistentConcurrentSessionRequests(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("{\n  \"messages\": [{\"role\":\"system\",\"content\":\"rendered skill 中文\"}]\n}\n")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			record, err := store.Begin(Scope{SessionID: "sess_test", TurnID: "item_1"}, Metadata{Provider: "openai", Model: "test"}, raw)
			if err != nil {
				t.Error(err)
				return
			}
			if err = record.Finish(200, "remote-id", "done"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if _, err = Open(root); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "sess_test"))
	if err != nil || len(entries) != 20 {
		t.Fatal(len(entries), err)
	}
	for _, entry := range entries {
		dir := filepath.Join(root, "sess_test", entry.Name())
		body, err := os.ReadFile(filepath.Join(dir, "request.json"))
		if err != nil || !bytes.Equal(body, raw) {
			t.Fatal("request changed", err)
		}
		data, err := os.ReadFile(filepath.Join(dir, "metadata.json"))
		if err != nil {
			t.Fatal(err)
		}
		var meta Metadata
		if err = json.Unmarshal(data, &meta); err != nil || meta.SessionID != "sess_test" || meta.Outcome != "done" || meta.HTTPStatus != 200 || meta.FinishedAt.IsZero() {
			t.Fatal(meta, err)
		}
		info, _ := os.Stat(filepath.Join(dir, "request.json"))
		if info.Mode().Perm() != 0600 {
			t.Fatal(info.Mode())
		}
	}
}
func TestUnfinishedAndUnscopedRequests(t *testing.T) {
	root := t.TempDir()
	store, _ := Open(root)
	record, err := store.Begin(Scope{}, Metadata{}, []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(root, "unscoped", record.ID(), "metadata.json")); err != nil {
		t.Fatal(err)
	}
	record, err = store.Begin(Scope{SessionID: "../../outside"}, Metadata{}, []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(filepath.Dir(record.dir)) != root {
		t.Fatal("session escaped root")
	}
	raw, _ := os.ReadFile(filepath.Join(record.dir, "metadata.json"))
	var meta Metadata
	json.Unmarshal(raw, &meta)
	if meta.Outcome != "prepared" || !meta.FinishedAt.IsZero() {
		t.Fatal("uncompleted request reported complete")
	}
}
