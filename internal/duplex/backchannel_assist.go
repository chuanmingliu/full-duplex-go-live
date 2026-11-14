package duplex

import (
	"strings"
	"time"

	"github.com/chuanmingliu/golive/internal/endpoint"
)

// detectorMayAssist only accelerates the existing ASR final+continuation path.
// It never yields the floor, accepts a caller, swallows speech or ends a turn.
func (e *Engine) detectorMayAssist(run *asrRun) bool {
	if e.detector == nil || e.opts.Cfg.Duplex.BackchannelDetectorMode != "asr_assist" || e.hold == nil || e.hold.kept || !e.hold.bargeIn || e.hangingUp() || run == nil {
		return false
	}
	if e.detectorAssisted && e.detectorAssistedStart == e.utterStartMS {
		return false
	}
	p := e.detector.Snapshot()
	if !p.Eligible(time.Now(), e.inputMS.Load(), e.utterStartMS, e.gen.Current()) {
		return false
	}
	if !e.detectorObserved || e.detectorObservedStart != e.utterStartMS {
		e.detectorObserved = true
		e.detectorObservedStart = e.utterStartMS
		e.log.Info("backchannel: floor evidence", "utterance_start_ms", e.utterStartMS, "floor_probability", p.Floor, "backchannel_probability", p.Backchannel, "frames", p.FloorFrames, "input_lag_ms", e.inputMS.Load()-p.EndMS, "asr_chars", len([]rune(run.lastText)))
	}
	// A known/potential acknowledgment retains its full original opportunity to
	// finish. Empty ASR is allowed only with positive, current caller evidence.
	text := strings.TrimSpace(run.lastText)
	if text != "" && (e.bcSet.CouldBecome(text) || endpoint.Hesitation(withoutForcedStop(text))) {
		return false
	}
	fs := e.focus
	if fs == nil || fs.distrust || !e.focusActs() {
		return false
	}
	a := fs.tracker.AttributeOver(e.utterStartMS, e.inputMS.Load())
	return a.Known && a.Coverage >= .6 && callerSpeaksOver(a)
}
