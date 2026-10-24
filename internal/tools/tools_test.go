package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemoryMergesPatchesAndCountsVersions(t *testing.T) {
	m := NewMemory(map[string]any{"carNum": "沪A·00000"})
	v, err := m.Patch(json.RawMessage(`{"quote":{"三者险":"200万","车损险":true}}`))
	if err != nil || v != 1 {
		t.Fatalf("v %d err %v", v, err)
	}
	v, _ = m.Patch(json.RawMessage(`{"quote":{"三者险":"300万","划痕险":false},"name":"王先生"}`))
	if v != 2 {
		t.Fatalf("v %d", v)
	}
	v, _ = m.Patch(json.RawMessage(`{"quote":{"划痕险":null}}`))
	snap, ver := m.Snapshot()
	var got map[string]any
	json.Unmarshal(snap, &got)
	q := got["quote"].(map[string]any)
	if ver != 3 || v != 3 || q["三者险"] != "300万" || q["车损险"] != true || q["划痕险"] != nil || got["carNum"] != "沪A·00000" || got["name"] != "王先生" {
		t.Fatalf("memory %s v%d", snap, ver)
	}
	if _, err := m.Patch(json.RawMessage(`[1,2]`)); err == nil {
		t.Fatal("an array was taken as a patch")
	}
}

func TestHTTPExecutorPostsTheRequestAndReturnsTheBody(t *testing.T) {
	var seen Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&seen)
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		w.Write([]byte(`{"total":1234.5}`))
	}))
	defer srv.Close()
	ex := &HTTPExecutor{Token: "tok"}
	res := ex.Execute(context.Background(), Tool{Name: "quote", URL: srv.URL}, Request{Tool: "quote", CallID: "c1", Arguments: json.RawMessage(`{"a":1}`), MemoryVersion: 3})
	if res.Err != nil || string(res.Output) != `{"total":1234.5}` || seen.CallID != "c1" || seen.MemoryVersion != 3 {
		t.Fatalf("res %+v seen %+v", res, seen)
	}
	// a non-JSON body is wrapped as a string
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	defer srv2.Close()
	res = ex.Execute(context.Background(), Tool{Name: "x", URL: srv2.URL}, Request{})
	if res.Err != nil || string(res.Output) != `"ok"` {
		t.Fatalf("res %+v", res)
	}
	// a 500 is an error with the body
	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", 500) }))
	defer srv3.Close()
	res = ex.Execute(context.Background(), Tool{Name: "x", URL: srv3.URL}, Request{})
	if res.Err == nil {
		t.Fatal("no error on 500")
	}
}

type slowExec struct {
	d     time.Duration
	calls atomic.Int32
}

func (s *slowExec) Execute(ctx context.Context, t Tool, req Request) Result {
	s.calls.Add(1)
	select {
	case <-time.After(s.d):
		return Result{Output: json.RawMessage(`{"done":true}`), Took: s.d}
	case <-ctx.Done():
		return Result{Err: ctx.Err()}
	}
}

func TestPoolRunsTasksOnBoundedWorkersAndReportsEach(t *testing.T) {
	ex := &slowExec{d: 100 * time.Millisecond}
	p := NewPool(ex, 2)
	var done atomic.Int32
	began := time.Now()
	for i := 0; i < 4; i++ {
		p.Submit(context.Background(), Tool{Name: "q", Async: true}, Request{}, func(task *Task) {
			if task.Result.Err == nil {
				done.Add(1)
			}
		})
	}
	if p.Running() != 4 {
		t.Fatalf("running %d", p.Running())
	}
	deadline := time.Now().Add(2 * time.Second)
	for done.Load() < 4 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if done.Load() != 4 {
		t.Fatalf("done %d", done.Load())
	}
	// two at a time: four 100 ms tasks take about 200 ms, not 100
	if d := time.Since(began); d < 190*time.Millisecond {
		t.Fatalf("four tasks on two workers took %v", d)
	}
}

func TestACancelledTaskIsNotReported(t *testing.T) {
	ex := &slowExec{d: 300 * time.Millisecond}
	p := NewPool(ex, 1)
	var reported atomic.Int32
	task := p.Submit(context.Background(), Tool{Name: "q", Async: true}, Request{}, func(*Task) { reported.Add(1) })
	time.Sleep(50 * time.Millisecond)
	task.Cancel()
	<-task.Done
	time.Sleep(50 * time.Millisecond)
	if reported.Load() != 0 || task.Result.Err == nil {
		t.Fatalf("reported %d err %v", reported.Load(), task.Result.Err)
	}
}

func TestDefinitionIsChatCompletionsShape(t *testing.T) {
	var d struct {
		Type     string `json:"type"`
		Function struct {
			Name       string          `json:"name"`
			Parameters json.RawMessage `json:"parameters"`
		} `json:"function"`
	}
	if err := json.Unmarshal(UpdateMemoryTool.Definition(), &d); err != nil || d.Type != "function" || d.Function.Name != "update_memory" || len(d.Function.Parameters) == 0 {
		t.Fatalf("%s", UpdateMemoryTool.Definition())
	}
}
