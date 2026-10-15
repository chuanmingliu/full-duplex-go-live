// Package tools is what the assistant can do besides talk: functions the
// chat model may call that golive itself carries out, rather than handing
// them to the application at the other end of the session.
//
// Two kinds matter on a phone call. A quick one — look something up, note
// something down — is run and its result given back to the model in the
// same breath. A slow one — pricing a policy, querying a system that takes
// seconds — must not hold the conversation: it is submitted to a pool, the
// model is told at once that it is running, the caller goes on talking, and
// when the result comes the engine finds a moment to say it. That is the
// whole point of the package: the conversation never waits on a tool.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Tool is one function the model may call.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	// URL is where the tool runs: POST, JSON in, JSON out (see Request and
	// the executor). Empty for a builtin.
	URL string `json:"url,omitempty"`
	// Async tools are submitted to the pool and answered "running" at once;
	// their result is delivered when it comes. Others are run and answered
	// in the same turn, within TimeoutMS.
	Async     bool `json:"async,omitempty"`
	TimeoutMS int  `json:"timeout_ms,omitempty"`
	// ETASeconds is what the model is told to expect for an async tool.
	ETASeconds int `json:"eta_s,omitempty"`
	// Builtin names a tool the engine implements itself ("update_memory").
	Builtin string `json:"builtin,omitempty"`
}

// Definition is the tool as the chat model sees it (chat-completions form).
func (t Tool) Definition() json.RawMessage {
	params := t.Parameters
	if len(params) == 0 {
		params = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	def := map[string]any{"type": "function", "function": map[string]any{
		"name": t.Name, "description": t.Description, "parameters": params}}
	b, _ := json.Marshal(def)
	return b
}

// Timeout is the tool's, or a default.
func (t Tool) Timeout() time.Duration {
	if t.TimeoutMS > 0 {
		return time.Duration(t.TimeoutMS) * time.Millisecond
	}
	if t.Async {
		return 60 * time.Second
	}
	return 8 * time.Second
}

// Request is what a tool's service is sent.
type Request struct {
	Tool      string          `json:"tool"`
	CallID    string          `json:"call_id"`
	Session   string          `json:"session"`
	Arguments json.RawMessage `json:"arguments"`
	// Memory is the call's working memory at the time of the call, and its
	// version, so a slow tool's result can say what it was computed on.
	Memory        json.RawMessage `json:"memory,omitempty"`
	MemoryVersion int             `json:"memory_version,omitempty"`
}

// Result is what comes back: the output the model is given, as JSON.
type Result struct {
	Output json.RawMessage
	Err    error
	// Took is how long the tool ran.
	Took time.Duration
}

// Executor runs a tool's request.
type Executor interface {
	Execute(ctx context.Context, t Tool, req Request) Result
}

// HTTPExecutor posts the request to the tool's URL.
type HTTPExecutor struct {
	Client *http.Client
	Token  string
}

// Execute implements Executor.
func (h *HTTPExecutor) Execute(ctx context.Context, t Tool, req Request) Result {
	began := time.Now()
	if t.URL == "" {
		return Result{Err: fmt.Errorf("tool %s has no URL", t.Name), Took: time.Since(began)}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return Result{Err: err}
	}
	ctx, cancel := context.WithTimeout(ctx, t.Timeout())
	defer cancel()
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, t.URL, bytes.NewReader(body))
	if err != nil {
		return Result{Err: err}
	}
	hr.Header.Set("Content-Type", "application/json")
	if h.Token != "" {
		hr.Header.Set("Authorization", "Bearer "+h.Token)
	}
	client := h.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(hr)
	if err != nil {
		return Result{Err: fmt.Errorf("tool %s: %w", t.Name, err), Took: time.Since(began)}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Result{Err: fmt.Errorf("tool %s: %w", t.Name, err), Took: time.Since(began)}
	}
	if resp.StatusCode/100 != 2 {
		return Result{Err: fmt.Errorf("tool %s: HTTP %d: %s", t.Name, resp.StatusCode, snippet(raw)), Took: time.Since(began)}
	}
	if !json.Valid(raw) {
		raw, _ = json.Marshal(string(raw))
	}
	return Result{Output: raw, Took: time.Since(began)}
}

func snippet(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// Task is one slow tool call in flight.
type Task struct {
	ID        string
	Tool      Tool
	Request   Request
	Submitted time.Time
	// Done is closed when Result is set.
	Done      chan struct{}
	Result    Result
	cancel    context.CancelFunc
	cancelled atomic.Bool
}

// Cancel stops the task; a cancelled task's result is never delivered.
func (t *Task) Cancel() {
	t.cancelled.Store(true)
	if t.cancel != nil {
		t.cancel()
	}
}

// Cancelled reports whether Cancel was called.
func (t *Task) Cancelled() bool { return t.cancelled.Load() }

// Pool runs slow tools on a bounded number of workers, so a burst of them
// queues rather than swamping the box, and reports each result to whoever
// submitted it.
type Pool struct {
	exec    Executor
	workers chan struct{}
	seq     atomic.Int64
	mu      sync.Mutex
	running map[string]*Task
}

// NewPool makes a pool of n workers.
func NewPool(exec Executor, n int) *Pool {
	if n < 1 {
		n = 1
	}
	return &Pool{exec: exec, workers: make(chan struct{}, n), running: map[string]*Task{}}
}

// Submit starts a task; done is called, from the pool's goroutine, when it
// finishes, unless it was cancelled.
func (p *Pool) Submit(ctx context.Context, t Tool, req Request, done func(*Task)) *Task {
	id := fmt.Sprintf("t%d", p.seq.Add(1))
	cctx, cancel := context.WithCancel(ctx)
	task := &Task{ID: id, Tool: t, Request: req, Submitted: time.Now(), Done: make(chan struct{}), cancel: cancel}
	p.mu.Lock()
	p.running[id] = task
	p.mu.Unlock()
	go func() {
		defer cancel()
		defer func() {
			p.mu.Lock()
			delete(p.running, id)
			p.mu.Unlock()
		}()
		// A worker slot; waiting for one counts against the timeout.
		select {
		case p.workers <- struct{}{}:
		case <-cctx.Done():
			task.Result = Result{Err: errors.New("cancelled before it ran")}
			close(task.Done)
			return
		}
		task.Result = p.exec.Execute(cctx, t, req)
		<-p.workers
		close(task.Done)
		if done != nil && !task.Cancelled() {
			done(task)
		}
	}()
	return task
}

// Running is how many tasks are in flight.
func (p *Pool) Running() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.running)
}

// Memory is a call's working memory: what has been settled so far, as a
// JSON object the model edits by merging patches, with a version that counts
// the edits. It is rendered into the model's context on every request, so
// the latest state is always in view and never has to be reconstructed from
// the conversation.
type Memory struct {
	mu      sync.Mutex
	data    map[string]any
	version int
	updated time.Time
}

// NewMemory starts from seed (may be nil).
func NewMemory(seed map[string]any) *Memory {
	m := &Memory{data: map[string]any{}}
	for k, v := range seed {
		m.data[k] = v
	}
	return m
}

// Patch merges a JSON object into the memory: keys are set, null deletes,
// nested objects merge one level down. It returns the new version.
func (m *Memory) Patch(patch json.RawMessage) (int, error) {
	var p map[string]any
	if err := json.Unmarshal(patch, &p); err != nil || p == nil {
		return 0, errors.New("the patch must be a JSON object")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	merge(m.data, p)
	m.version++
	m.updated = time.Now()
	return m.version, nil
}

func merge(dst, src map[string]any) {
	for k, v := range src {
		if v == nil {
			delete(dst, k)
			continue
		}
		if sv, ok := v.(map[string]any); ok {
			if dv, ok := dst[k].(map[string]any); ok {
				merge(dv, sv)
				continue
			}
			cp := map[string]any{}
			merge(cp, sv)
			dst[k] = cp
			continue
		}
		dst[k] = v
	}
}

// Snapshot is the memory as JSON and its version.
func (m *Memory) Snapshot() (json.RawMessage, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, _ := json.Marshal(m.data)
	return b, m.version
}

// Version is the count of edits so far.
func (m *Memory) Version() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.version
}

// Empty reports whether nothing has been settled.
func (m *Memory) Empty() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.data) == 0
}

// UpdateMemoryTool is the builtin the model edits the memory with.
var UpdateMemoryTool = Tool{
	Name: "update_memory",
	Description: "记录或修改本次通话已经确定的信息（工作记忆）。每当用户确认、新增或更改了投保方案、车辆信息、联系方式等事实时调用；" +
		"传入要合并的字段，值为 null 表示删除该字段。当前工作记忆会随时出现在你的上下文中，不必复述。",
	Parameters: json.RawMessage(`{"type":"object","properties":{"patch":{"type":"object","description":"要合并进工作记忆的字段"}},"required":["patch"]}`),
	Builtin:    "update_memory",
}
