// Package metrics is a tiny in-process registry exposed as Prometheus text.
//
// No client library: the service has one runtime dependency and this is not
// worth a second. The surface is the numbers an operator needs to know whether
// a box is taking calls, rejecting them, or getting slower.
package metrics

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
)

// Histogram buckets for first-audio latency, in milliseconds. They sit around
// the cascade's real range: VAD hangover (~380) plus a fast backend (~300) up
// through a long persona prompt (several seconds).
var latencyBuckets = []int64{200, 400, 600, 800, 1000, 1500, 2000, 3000, 5000, 8000}

// Registry holds process-wide counters. Safe for concurrent use.
type Registry struct {
	sessionsActive  atomic.Int64
	sessionsStarted atomic.Int64
	sessionsClosed  atomic.Int64
	audioDrops      atomic.Int64
	turns           atomic.Int64

	rejected sync.Map // reason -> *atomic.Int64
	errors   sync.Map // code -> *atomic.Int64
	retries  sync.Map // kind -> *atomic.Int64
	provErrs sync.Map // kind -> *atomic.Int64

	bargeIns atomic.Int64

	histN      atomic.Int64
	histSumMS  atomic.Int64
	histCounts []atomic.Int64 // len(latencyBuckets)+1, +Inf last

	buildMu      sync.Mutex
	buildVersion string
	buildCommit  string
}

// New builds an empty registry.
func New() *Registry {
	r := &Registry{histCounts: make([]atomic.Int64, len(latencyBuckets)+1)}
	return r
}

var (
	defaultMu  sync.RWMutex
	defaultReg = New()
)

// Default is the process-wide registry the server and engine share.
func Default() *Registry {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultReg
}

// ResetDefault replaces the process registry. Tests use it so they do not
// inherit counts from each other.
func ResetDefault() {
	defaultMu.Lock()
	defaultReg = New()
	defaultMu.Unlock()
}

func (r *Registry) bump(m *sync.Map, key string) {
	if key == "" {
		key = "unknown"
	}
	v, _ := m.LoadOrStore(key, &atomic.Int64{})
	v.(*atomic.Int64).Add(1)
}

// SessionOpen records a live session.
func (r *Registry) SessionOpen() {
	r.sessionsActive.Add(1)
	r.sessionsStarted.Add(1)
}

// SessionClose records a session ending.
func (r *Registry) SessionClose() {
	r.sessionsActive.Add(-1)
	r.sessionsClosed.Add(1)
}

// Rejected records a connection that never became a session.
func (r *Registry) Rejected(reason string) { r.bump(&r.rejected, reason) }

// Error records a protocol-level error code.
func (r *Registry) Error(code string) { r.bump(&r.errors, code) }

// AudioDropped records an input frame the engine could not keep.
func (r *Registry) AudioDropped() { r.audioDrops.Add(1) }

// Retry records one automatic retry of a provider call.
func (r *Registry) Retry(kind string) { r.bump(&r.retries, kind) }

// ProviderError records a provider call that failed after retries.
func (r *Registry) ProviderError(kind string) { r.bump(&r.provErrs, kind) }

// BargeIn records an answer cut short by user speech.
func (r *Registry) BargeIn() { r.bargeIns.Add(1) }

// SetBuild stamps the process identity onto /metrics.
func (r *Registry) SetBuild(version, commit string) {
	r.buildMu.Lock()
	r.buildVersion = version
	r.buildCommit = commit
	r.buildMu.Unlock()
}

// ObserveTurn records one completed (or truncated) turn's time-to-first-audio.
func (r *Registry) ObserveTurn(firstAudioOutMS int64) {
	r.turns.Add(1)
	if firstAudioOutMS < 0 {
		// Speculative turns can go negative; clamp for the histogram so the
		// bucket still means "the caller heard something immediately".
		firstAudioOutMS = 0
	}
	r.histN.Add(1)
	r.histSumMS.Add(firstAudioOutMS)
	placed := false
	for i, b := range latencyBuckets {
		if firstAudioOutMS <= b {
			r.histCounts[i].Add(1)
			placed = true
			break
		}
	}
	if !placed {
		r.histCounts[len(latencyBuckets)].Add(1)
	}
}

// ActiveSessions is the current gauge.
func (r *Registry) ActiveSessions() int64 { return r.sessionsActive.Load() }

// WritePrometheus emits OpenMetrics-ish text.
func (r *Registry) WritePrometheus(w io.Writer) {
	fmt.Fprintf(w, "# HELP golive_sessions_active Open WebSocket sessions.\n")
	fmt.Fprintf(w, "# TYPE golive_sessions_active gauge\n")
	fmt.Fprintf(w, "golive_sessions_active %d\n", r.sessionsActive.Load())

	fmt.Fprintf(w, "# HELP golive_sessions_started_total Sessions that passed admission.\n")
	fmt.Fprintf(w, "# TYPE golive_sessions_started_total counter\n")
	fmt.Fprintf(w, "golive_sessions_started_total %d\n", r.sessionsStarted.Load())

	fmt.Fprintf(w, "# HELP golive_sessions_closed_total Sessions that have ended.\n")
	fmt.Fprintf(w, "# TYPE golive_sessions_closed_total counter\n")
	fmt.Fprintf(w, "golive_sessions_closed_total %d\n", r.sessionsClosed.Load())

	fmt.Fprintf(w, "# HELP golive_sessions_rejected_total Upgrades refused before a session started.\n")
	fmt.Fprintf(w, "# TYPE golive_sessions_rejected_total counter\n")
	writeLabeled(w, "golive_sessions_rejected_total", "reason", &r.rejected)

	fmt.Fprintf(w, "# HELP golive_errors_total Protocol errors emitted to clients.\n")
	fmt.Fprintf(w, "# TYPE golive_errors_total counter\n")
	writeLabeled(w, "golive_errors_total", "code", &r.errors)

	fmt.Fprintf(w, "# HELP golive_audio_frames_dropped_total Input frames dropped because the engine was behind.\n")
	fmt.Fprintf(w, "# TYPE golive_audio_frames_dropped_total counter\n")
	fmt.Fprintf(w, "golive_audio_frames_dropped_total %d\n", r.audioDrops.Load())

	fmt.Fprintf(w, "# HELP golive_provider_retries_total Provider calls retried once after a blip.\n")
	fmt.Fprintf(w, "# TYPE golive_provider_retries_total counter\n")
	writeLabeled(w, "golive_provider_retries_total", "kind", &r.retries)

	fmt.Fprintf(w, "# HELP golive_provider_errors_total Provider calls that failed after retries.\n")
	fmt.Fprintf(w, "# TYPE golive_provider_errors_total counter\n")
	writeLabeled(w, "golive_provider_errors_total", "kind", &r.provErrs)

	fmt.Fprintf(w, "# HELP golive_barge_ins_total Answers cut short by user speech.\n")
	fmt.Fprintf(w, "# TYPE golive_barge_ins_total counter\n")
	fmt.Fprintf(w, "golive_barge_ins_total %d\n", r.bargeIns.Load())

	r.buildMu.Lock()
	ver, commit := r.buildVersion, r.buildCommit
	r.buildMu.Unlock()
	if ver != "" || commit != "" {
		fmt.Fprintf(w, "# HELP golive_build_info Build identity.\n")
		fmt.Fprintf(w, "# TYPE golive_build_info gauge\n")
		fmt.Fprintf(w, "golive_build_info{version=%q,commit=%q} 1\n", sanitize(ver), sanitize(commit))
	}

	fmt.Fprintf(w, "# HELP golive_turns_total Assistant turns that produced metrics.\n")
	fmt.Fprintf(w, "# TYPE golive_turns_total counter\n")
	fmt.Fprintf(w, "golive_turns_total %d\n", r.turns.Load())

	fmt.Fprintf(w, "# HELP golive_turn_first_audio_ms Time from VAD close to first output audio byte.\n")
	fmt.Fprintf(w, "# TYPE golive_turn_first_audio_ms histogram\n")
	var cum int64
	for i, b := range latencyBuckets {
		cum += r.histCounts[i].Load()
		fmt.Fprintf(w, "golive_turn_first_audio_ms_bucket{le=\"%d\"} %d\n", b, cum)
	}
	cum += r.histCounts[len(latencyBuckets)].Load()
	fmt.Fprintf(w, "golive_turn_first_audio_ms_bucket{le=\"+Inf\"} %d\n", cum)
	fmt.Fprintf(w, "golive_turn_first_audio_ms_sum %d\n", r.histSumMS.Load())
	fmt.Fprintf(w, "golive_turn_first_audio_ms_count %d\n", r.histN.Load())
}

func writeLabeled(w io.Writer, metric, label string, m *sync.Map) {
	empty := true
	m.Range(func(k, v any) bool {
		empty = false
		fmt.Fprintf(w, "%s{%s=%q} %d\n", metric, label, sanitize(k.(string)), v.(*atomic.Int64).Load())
		return true
	})
	if empty {
		fmt.Fprintf(w, "%s{dummy=\"none\"} 0\n", metric)
	}
}

func sanitize(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}
