//! Energy/ZCR VAD ported from internal/audio/vad.go.
//! Time advances on received audio, with candidate gaps and a nonduplicating preroll.
use crate::audio::{dbfs, rms, zero_crossing_rate};
use serde::{Deserialize, Serialize};
use std::collections::VecDeque;

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(default)]
pub struct VadConfig {
    pub sample_rate: u32,
    pub frame_ms: i32,
    pub min_speech_ms: i32,
    pub min_silence_ms: i32,
    pub speech_pad_ms: i32,
    pub max_speech_ms: i32,
    pub snapshot_ms: i32,
    pub noise_floor_db: f64,
    pub max_noise_floor_db: f64,
    pub speech_margin_db: f64,
    pub max_zcr: f64,
    pub gap_tolerance_ms: i32,
    pub barge_in_margin_db: f64,
    pub barge_in_min_speech_ms: i32,
    pub floor_track_ms: i32,
    #[serde(alias = "gate_trace_ms")]
    pub trace_ms: i32,
    pub echo_tail_ms: i32,
    pub line_fill_ms: i32,
}
impl Default for VadConfig {
    fn default() -> Self {
        Self {
            sample_rate: 16000,
            frame_ms: 20,
            min_speech_ms: 200,
            min_silence_ms: 380,
            speech_pad_ms: 240,
            max_speech_ms: 30000,
            snapshot_ms: 40,
            noise_floor_db: -60.0,
            max_noise_floor_db: -28.0,
            speech_margin_db: 10.0,
            max_zcr: 0.55,
            gap_tolerance_ms: 160,
            barge_in_margin_db: 6.0,
            barge_in_min_speech_ms: 320,
            floor_track_ms: 2000,
            trace_ms: 100,
            echo_tail_ms: 0,
            line_fill_ms: 0,
        }
    }
}
impl VadConfig {
    fn apply_defaults(&mut self) {
        let d = Self::default();
        if self.sample_rate == 0 {
            self.sample_rate = d.sample_rate;
        }
        if self.frame_ms <= 0 {
            self.frame_ms = d.frame_ms;
        }
        if self.min_speech_ms <= 0 {
            self.min_speech_ms = d.min_speech_ms;
        }
        if self.min_silence_ms <= 0 {
            self.min_silence_ms = d.min_silence_ms;
        }
        if self.speech_pad_ms < 0 {
            self.speech_pad_ms = d.speech_pad_ms;
        }
        if self.max_speech_ms <= 0 {
            self.max_speech_ms = d.max_speech_ms;
        }
        if self.snapshot_ms <= 0 {
            self.snapshot_ms = d.snapshot_ms;
        }
        if self.noise_floor_db == 0.0 || !self.noise_floor_db.is_finite() {
            self.noise_floor_db = d.noise_floor_db;
        }
        if self.max_noise_floor_db == 0.0
            || self.max_noise_floor_db < self.noise_floor_db
            || !self.max_noise_floor_db.is_finite()
        {
            self.max_noise_floor_db = d.max_noise_floor_db;
        }
        if self.speech_margin_db <= 0.0 || !self.speech_margin_db.is_finite() {
            self.speech_margin_db = d.speech_margin_db;
        }
        if self.max_zcr <= 0.0 || !self.max_zcr.is_finite() {
            self.max_zcr = d.max_zcr;
        }
        if self.gap_tolerance_ms <= 0 {
            self.gap_tolerance_ms = d.gap_tolerance_ms;
        }
        if self.barge_in_margin_db < 0.0 || !self.barge_in_margin_db.is_finite() {
            self.barge_in_margin_db = d.barge_in_margin_db;
        }
        if self.barge_in_min_speech_ms <= 0 {
            self.barge_in_min_speech_ms = d.barge_in_min_speech_ms;
        }
        if self.floor_track_ms == 0 {
            self.floor_track_ms = d.floor_track_ms;
        }
        if self.trace_ms == 0 {
            self.trace_ms = d.trace_ms;
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub enum DecisionKind {
    #[default]
    None,
    Started,
    Speaking,
    Stopped,
}
#[derive(Debug, Clone, Default)]
pub struct Decision {
    pub kind: DecisionKind,
    pub utterance: Vec<f32>,
    pub snapshot: Vec<f32>,
    pub active_ms: f64,
    pub start_ms: i64,
    pub end_ms: i64,
    pub barge_in: bool,
    pub silence_ms: f64,
}
#[derive(Debug, Clone, Default, Serialize)]
pub struct GateSample {
    #[serde(rename = "t")]
    pub ms: i64,
    #[serde(rename = "hi")]
    pub hi: f64,
    #[serde(rename = "lo")]
    pub lo: f64,
    #[serde(rename = "f")]
    pub floor: f64,
    #[serde(rename = "b")]
    pub bar: f64,
    #[serde(rename = "v")]
    pub voiced_ms: i32,
    #[serde(rename = "o")]
    pub open: bool,
    #[serde(rename = "s")]
    pub speaking: bool,
    #[serde(rename = "r")]
    pub raised: bool,
}
#[derive(Debug, Clone)]
pub struct EchoMiss {
    pub reason: &'static str,
    pub quiet_ms: f64,
    pub level_db: f64,
    pub noise_floor_db: f64,
}

pub struct Vad {
    cfg: VadConfig,
    frame_samples: usize,
    frame_ms: f64,
    preroll: VecDeque<Vec<f32>>,
    preroll_cap: usize,
    open: bool,
    barge_in: bool,
    utterance: Vec<f32>,
    active_ms: f64,
    silence_ms: f64,
    pending_ms: f64,
    pending_gap_ms: f64,
    pending: Vec<f32>,
    snapshot_acc: Vec<f32>,
    since_snap: f64,
    voiced_run_ms: f64,
    min_silence_ms: i32,
    start_ms: i64,
    clock_ms: f64,
    noise_floor_db: f64,
    echo_floor_db: f64,
    speaking: bool,
    echo_tail_until: f64,
    echo_arm_at: Option<f64>,
    quiet_ms: f64,
    echo_armed_at: Option<f64>,
    echo_return: Option<(f64, f64)>,
    echo_miss: Option<EchoMiss>,
    block_min: f64,
    block_ms: f64,
    block_mins: Vec<f64>,
    block_pos: usize,
    block_count: usize,
    floor_raise: Option<(f64, f64)>,
    trace: Vec<GateSample>,
    current: Option<GateSample>,
    current_ms: f64,
    current_raised: bool,
}
impl Vad {
    pub fn new(mut cfg: VadConfig) -> Self {
        cfg.apply_defaults();
        let frame_samples =
            (u64::from(cfg.sample_rate) * cfg.frame_ms as u64 / 1000).max(1) as usize;
        let preroll_cap = (cfg.speech_pad_ms / cfg.frame_ms).max(1) as usize;
        Self {
            frame_samples,
            preroll_cap,
            frame_ms: f64::from(cfg.frame_ms),
            noise_floor_db: cfg.noise_floor_db,
            echo_floor_db: cfg.noise_floor_db,
            cfg,
            preroll: VecDeque::new(),
            open: false,
            barge_in: false,
            utterance: Vec::new(),
            active_ms: 0.0,
            silence_ms: 0.0,
            pending_ms: 0.0,
            pending_gap_ms: 0.0,
            pending: Vec::new(),
            snapshot_acc: Vec::new(),
            since_snap: 0.0,
            voiced_run_ms: 0.0,
            min_silence_ms: 0,
            start_ms: 0,
            clock_ms: 0.0,
            speaking: false,
            echo_tail_until: 0.0,
            echo_arm_at: None,
            quiet_ms: 0.0,
            echo_armed_at: None,
            echo_return: None,
            echo_miss: None,
            block_min: 0.0,
            block_ms: 0.0,
            block_mins: Vec::new(),
            block_pos: 0,
            block_count: 0,
            floor_raise: None,
            trace: Vec::new(),
            current: None,
            current_ms: 0.0,
            current_raised: false,
        }
    }
    pub fn frame_samples(&self) -> usize {
        self.frame_samples
    }
    pub fn frame_bytes(&self) -> usize {
        self.frame_samples * 2
    }
    pub fn frame_ms(&self) -> f64 {
        self.frame_ms
    }
    pub fn clock_ms(&self) -> f64 {
        self.clock_ms
    }
    pub fn elapsed_ms(&self) -> i64 {
        self.clock_ms as i64
    }
    pub fn is_active(&self) -> bool {
        self.open
    }
    pub fn is_speaking(&self) -> bool {
        self.speaking
    }
    pub fn active_ms(&self) -> f64 {
        if self.open { self.active_ms } else { 0.0 }
    }
    pub fn candidate_ms(&self) -> f64 {
        if self.open { 0.0 } else { self.pending_ms }
    }
    pub fn trailing_silence_ms(&self) -> f64 {
        if self.open { self.silence_ms } else { 0.0 }
    }
    pub fn voiced_run_ms(&self) -> f64 {
        if self.open { self.voiced_run_ms } else { 0.0 }
    }
    pub fn noise_floor_db(&self) -> f64 {
        self.noise_floor_db
    }
    pub fn echo_floor_db(&self) -> f64 {
        self.echo_floor_db
    }
    pub fn floor_track_ms(&self) -> i32 {
        self.cfg.floor_track_ms
    }
    pub fn set_min_silence_ms(&mut self, ms: i32) {
        self.min_silence_ms = ms.max(0);
    }
    pub fn min_silence_ms(&self) -> i32 {
        if self.min_silence_ms > 0 {
            self.min_silence_ms
        } else {
            self.cfg.min_silence_ms
        }
    }
    pub fn trace(&self) -> &[GateSample] {
        &self.trace
    }
    pub fn take_floor_raise(&mut self) -> Option<(f64, f64)> {
        self.floor_raise.take()
    }
    pub fn take_echo_return(&mut self) -> Option<(f64, f64)> {
        self.echo_return.take()
    }
    pub fn take_echo_miss(&mut self) -> Option<EchoMiss> {
        self.echo_miss.take()
    }
    pub fn set_assistant_speaking(&mut self, speaking: bool) {
        if !speaking && self.speaking && self.cfg.echo_tail_ms > 0 {
            self.echo_tail_until = self.clock_ms + f64::from(self.cfg.echo_tail_ms);
        }
        if speaking && !self.speaking {
            self.echo_arm_at = Some(self.clock_ms);
        }
        self.speaking = speaking;
    }
    fn assistant_audible(&self) -> bool {
        self.speaking || self.clock_ms < self.echo_tail_until
    }
    fn thresholds(&self) -> (f64, i32) {
        if self.assistant_audible() {
            (
                self.cfg.speech_margin_db + self.cfg.barge_in_margin_db,
                self.cfg.min_speech_ms.max(self.cfg.barge_in_min_speech_ms),
            )
        } else {
            (self.cfg.speech_margin_db, self.cfg.min_speech_ms)
        }
    }
    pub fn reset(&mut self) {
        self.open = false;
        self.barge_in = false;
        self.utterance.clear();
        self.pending.clear();
        self.snapshot_acc.clear();
        self.active_ms = 0.0;
        self.pending_ms = 0.0;
        self.pending_gap_ms = 0.0;
        self.silence_ms = 0.0;
        self.since_snap = 0.0;
        self.voiced_run_ms = 0.0;
        self.min_silence_ms = 0;
    }
    pub fn stop(&mut self) -> Decision {
        if !self.open {
            return Decision::default();
        }
        let decision = Decision {
            kind: DecisionKind::Stopped,
            utterance: std::mem::take(&mut self.utterance),
            snapshot: std::mem::take(&mut self.snapshot_acc),
            active_ms: self.active_ms,
            start_ms: self.start_ms,
            end_ms: self.clock_ms as i64,
            barge_in: self.barge_in,
            silence_ms: self.silence_ms,
        };
        self.reset();
        decision
    }
    pub fn push(&mut self, frame: &[f32]) -> Decision {
        let fixed;
        let frame = if frame.len() != self.frame_samples {
            let mut f = vec![0.0; self.frame_samples];
            let n = frame.len().min(f.len());
            f[..n].copy_from_slice(&frame[..n]);
            fixed = f;
            &fixed[..]
        } else {
            frame
        };
        let frame_start = self.clock_ms;
        self.clock_ms += self.frame_ms;
        let level = dbfs(rms(frame));
        let zcr = zero_crossing_rate(frame);
        let (margin, min_speech) = self.thresholds();
        self.track_echo_return(frame_start, level);
        let voiced = level > self.noise_floor_db + margin && zcr <= self.cfg.max_zcr;
        // Echo level is diagnostic only: raising the speech gate from it would deafen a caller.
        if self.speaking && !self.open {
            if level > self.echo_floor_db {
                self.echo_floor_db += (level - self.echo_floor_db) * 0.05;
            }
            if !self.echo_floor_db.is_finite() {
                self.echo_floor_db = self.cfg.noise_floor_db;
            } else {
                self.echo_floor_db = self.echo_floor_db.min(self.cfg.max_noise_floor_db);
            }
        } else if !self.speaking {
            self.echo_floor_db += (self.cfg.noise_floor_db - self.echo_floor_db) * 0.002;
        }
        if !voiced && !self.open {
            self.noise_floor_db += (level - self.noise_floor_db)
                * if level > self.noise_floor_db {
                    0.02
                } else {
                    0.25
                };
            if !self.noise_floor_db.is_finite() {
                self.noise_floor_db = self.cfg.noise_floor_db;
            } else {
                self.noise_floor_db = self
                    .noise_floor_db
                    .clamp(self.cfg.noise_floor_db, self.cfg.max_noise_floor_db);
            }
        }
        self.track_floor(level, margin);
        self.sample(level, voiced);
        if self.open {
            self.push_open(frame, voiced)
        } else {
            self.push_closed(frame, voiced, min_speech)
        }
    }
    fn push_closed(&mut self, frame: &[f32], voiced: bool, min_speech: i32) -> Decision {
        if !voiced {
            if self.pending_ms > 0.0 {
                self.pending_gap_ms += self.frame_ms;
                if self.pending_gap_ms > f64::from(self.cfg.gap_tolerance_ms) {
                    let pending = std::mem::take(&mut self.pending);
                    for frame in pending.chunks_exact(self.frame_samples) {
                        self.push_preroll(frame);
                    }
                    self.pending_ms = 0.0;
                    self.pending_gap_ms = 0.0;
                    self.push_preroll(frame);
                } else {
                    self.pending.extend_from_slice(frame);
                }
            } else {
                self.push_preroll(frame);
            }
            return Decision::default();
        }
        self.pending_gap_ms = 0.0;
        self.pending.extend_from_slice(frame);
        self.pending_ms += self.frame_ms;
        if self.pending_ms < f64::from(min_speech) {
            return Decision::default();
        }
        self.open = true;
        self.barge_in = self.speaking;
        self.utterance = self.preroll.drain(..).flatten().collect();
        self.utterance.append(&mut self.pending);
        self.active_ms = self.pending_ms;
        self.silence_ms = 0.0;
        self.pending_ms = 0.0;
        self.pending_gap_ms = 0.0;
        self.snapshot_acc.clear();
        self.since_snap = 0.0;
        self.voiced_run_ms = self.active_ms;
        let held_ms = self.utterance.len() as f64 / f64::from(self.cfg.sample_rate) * 1000.0;
        self.start_ms = (self.clock_ms - held_ms).max(0.0) as i64;
        Decision {
            kind: DecisionKind::Started,
            snapshot: self.utterance.clone(),
            active_ms: self.active_ms,
            start_ms: self.start_ms,
            end_ms: self.clock_ms as i64,
            barge_in: self.barge_in,
            ..Default::default()
        }
    }
    fn push_open(&mut self, frame: &[f32], voiced: bool) -> Decision {
        self.utterance.extend_from_slice(frame);
        self.snapshot_acc.extend_from_slice(frame);
        self.since_snap += self.frame_ms;
        if voiced {
            self.active_ms += self.frame_ms;
            self.silence_ms = 0.0;
            self.voiced_run_ms += self.frame_ms;
        } else {
            self.silence_ms += self.frame_ms;
            self.voiced_run_ms = 0.0;
        }
        let total_ms = self.utterance.len() as f64 / f64::from(self.cfg.sample_rate) * 1000.0;
        if self.silence_ms >= f64::from(self.min_silence_ms())
            || total_ms >= f64::from(self.cfg.max_speech_ms)
        {
            return self.stop();
        }
        if self.since_snap >= f64::from(self.cfg.snapshot_ms) {
            self.since_snap = 0.0;
            return Decision {
                kind: DecisionKind::Speaking,
                snapshot: std::mem::take(&mut self.snapshot_acc),
                active_ms: self.active_ms,
                start_ms: self.start_ms,
                end_ms: self.clock_ms as i64,
                barge_in: self.barge_in,
                ..Default::default()
            };
        }
        Decision::default()
    }
    fn push_preroll(&mut self, frame: &[f32]) {
        if self.preroll.len() == self.preroll_cap {
            self.preroll.pop_front();
        }
        self.preroll.push_back(frame.to_vec());
    }
    fn miss_echo(&mut self, reason: &'static str, level: f64) {
        self.echo_miss = Some(EchoMiss {
            reason,
            quiet_ms: self.quiet_ms,
            level_db: level,
            noise_floor_db: self.noise_floor_db,
        });
    }
    fn track_echo_return(&mut self, frame_start: f64, level: f64) {
        let loud = level > self.noise_floor_db + 8.0;
        if let Some(at) = self.echo_arm_at.take() {
            self.echo_armed_at = if self.quiet_ms >= 200.0 && !self.open {
                Some(at)
            } else {
                None
            };
            if self.open {
                self.miss_echo(
                    "the caller was mid-utterance when the assistant began",
                    level,
                );
            } else if self.echo_armed_at.is_none() {
                self.miss_echo(
                    "the line had not been quiet when the assistant began",
                    level,
                );
            }
        }
        if let Some(at) = self.echo_armed_at {
            let waited = frame_start - at;
            if waited > 1500.0 {
                self.echo_armed_at = None;
                self.miss_echo("nothing came back within the window", level);
            } else if loud {
                if waited >= 20.0 {
                    self.echo_return = Some((waited, level));
                } else {
                    self.miss_echo("a sound arrived in the frame the assistant began in", level);
                }
                self.echo_armed_at = None;
            }
        }
        if loud {
            self.quiet_ms = 0.0;
        } else {
            self.quiet_ms += self.frame_ms;
        }
    }
    fn track_floor(&mut self, level: f64, margin: f64) {
        if self.cfg.floor_track_ms <= 0 {
            return;
        }
        if self.block_ms == 0.0 || level < self.block_min {
            self.block_min = level;
        }
        self.block_ms += self.frame_ms;
        if self.block_ms < 100.0 {
            return;
        }
        let n = (self.cfg.floor_track_ms / 100).max(2) as usize;
        if self.block_mins.len() != n {
            self.block_mins = vec![0.0; n];
            self.block_pos = 0;
            self.block_count = 0;
        }
        self.block_mins[self.block_pos] = self.block_min;
        self.block_pos = (self.block_pos + 1) % n;
        self.block_count = (self.block_count + 1).min(n);
        self.block_ms = 0.0;
        if !self.open || self.block_count < n {
            return;
        }
        let quietest = self
            .block_mins
            .iter()
            .copied()
            .fold(f64::INFINITY, f64::min);
        if quietest <= self.noise_floor_db + margin {
            return;
        }
        let to = quietest.min(self.cfg.max_noise_floor_db);
        if to <= self.noise_floor_db {
            return;
        }
        self.floor_raise = Some((self.noise_floor_db, to));
        self.noise_floor_db = to;
        self.current_raised = true;
        self.block_count = 0;
    }
    fn sample(&mut self, level: f64, voiced: bool) {
        if self.cfg.trace_ms <= 0 {
            return;
        }
        let current = self.current.get_or_insert_with(|| GateSample {
            ms: (self.clock_ms - self.frame_ms) as i64,
            hi: level,
            lo: level,
            ..Default::default()
        });
        current.hi = current.hi.max(level);
        current.lo = current.lo.min(level);
        if voiced {
            current.voiced_ms += self.frame_ms as i32;
        }
        self.current_ms += self.frame_ms;
        if self.current_ms < f64::from(self.cfg.trace_ms) {
            return;
        }
        let (margin, _) = self.thresholds();
        let mut current = self.current.take().expect("sample exists");
        current.floor = round1(self.noise_floor_db);
        current.bar = round1(self.noise_floor_db + margin);
        current.hi = round1(current.hi);
        current.lo = round1(current.lo);
        current.open = self.open;
        current.speaking = self.speaking;
        current.raised = self.current_raised;
        if self.trace.len() < 72_000 {
            self.trace.push(current);
        }
        self.current_ms = 0.0;
        self.current_raised = false;
    }
}
fn round1(value: f64) -> f64 {
    if value.is_finite() {
        (value * 10.0).round() / 10.0
    } else {
        -99.0
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    fn tone(samples: usize, amplitude: f32) -> Vec<f32> {
        (0..samples)
            .map(|i| (i as f32 * std::f32::consts::TAU * 400.0 / 16000.0).sin() * amplitude)
            .collect()
    }
    fn feed(vad: &mut Vad, ms: i32, amplitude: f32) -> Vec<Decision> {
        let frame = tone(vad.frame_samples(), amplitude);
        (0..ms / 20)
            .map(|_| vad.push(&frame))
            .filter(|d| d.kind != DecisionKind::None)
            .collect()
    }
    #[test]
    fn snapshots_reconstruct_exactly_one_padded_utterance() {
        let mut vad = Vad::new(VadConfig::default());
        feed(&mut vad, 400, 0.0);
        let mut events = feed(&mut vad, 600, 0.1);
        events.extend(feed(&mut vad, 380, 0.0));
        assert_eq!(events.first().unwrap().kind, DecisionKind::Started);
        let stopped = events.last().unwrap();
        assert_eq!(stopped.kind, DecisionKind::Stopped);
        assert_eq!(stopped.start_ms, 160);
        assert_eq!(stopped.end_ms, 1380);
        assert_eq!(stopped.silence_ms, 380.0);
        assert_eq!(stopped.utterance.len(), (240 + 600 + 380) * 16);
        let joined: Vec<_> = events
            .iter()
            .flat_map(|d| d.snapshot.iter().copied())
            .collect();
        assert_eq!(joined, stopped.utterance);
        assert!(!vad.is_active());
    }
    #[test]
    fn short_coughs_do_not_accumulate_across_a_long_gap() {
        let mut vad = Vad::new(VadConfig::default());
        assert!(feed(&mut vad, 100, 0.1).is_empty());
        assert_eq!(vad.candidate_ms(), 100.0);
        assert!(feed(&mut vad, 180, 0.0).is_empty());
        assert_eq!(vad.candidate_ms(), 0.0);
        assert!(feed(&mut vad, 100, 0.1).is_empty());
        assert!(!vad.is_active());
        assert!(feed(&mut vad, 100, 0.0).is_empty());
        assert_eq!(feed(&mut vad, 100, 0.1)[0].kind, DecisionKind::Started);
    }
    #[test]
    fn discarded_candidate_remains_in_preroll_without_duplication() {
        let mut vad = Vad::new(VadConfig::default());
        let mut input = Vec::new();
        let mut events = Vec::new();
        for (ms, amplitude) in [(400, 0.0), (100, 0.1), (200, 0.0), (300, 0.2), (380, 0.0)] {
            for _ in 0..ms / 20 {
                let frame = tone(320, amplitude);
                input.extend_from_slice(&frame);
                let d = vad.push(&frame);
                if d.kind != DecisionKind::None {
                    events.push(d);
                }
            }
        }
        let d = events.last().unwrap();
        assert_eq!(d.kind, DecisionKind::Stopped);
        assert_eq!(
            d.utterance,
            input[d.start_ms as usize * 16..d.end_ms as usize * 16]
        );
    }
    #[test]
    fn interruption_threshold_and_echo_tail_hold_then_release() {
        // Pin the adaptive floor to isolate the raised gate from floor learning.
        let mut vad = Vad::new(VadConfig {
            barge_in_margin_db: 16.0,
            echo_tail_ms: 600,
            max_noise_floor_db: -60.0,
            ..Default::default()
        });
        vad.set_assistant_speaking(true);
        assert!(feed(&mut vad, 400, 0.02).is_empty());
        vad.set_assistant_speaking(false);
        assert!(feed(&mut vad, 400, 0.02).is_empty());
        let events = feed(&mut vad, 600, 0.02);
        assert!(events.iter().any(|d| d.kind == DecisionKind::Started));
    }
    #[test]
    fn a_real_barge_in_still_opens_and_marks_the_utterance() {
        let mut vad = Vad::new(VadConfig::default());
        vad.set_assistant_speaking(true);
        let events = feed(&mut vad, 320, 0.4);
        assert_eq!(events.len(), 1);
        assert!(events[0].barge_in);
        assert_eq!(events[0].active_ms, 320.0);
    }
    #[test]
    fn semantic_patience_is_reset_when_utterance_ends() {
        let mut vad = Vad::new(VadConfig::default());
        feed(&mut vad, 300, 0.1);
        vad.set_min_silence_ms(700);
        feed(&mut vad, 400, 0.0);
        assert!(vad.is_active());
        assert_eq!(vad.trailing_silence_ms(), 400.0);
        let stop = vad.stop();
        assert_eq!(stop.kind, DecisionKind::Stopped);
        assert_eq!(stop.silence_ms, 400.0);
        assert_eq!(vad.min_silence_ms(), 380);
        assert_eq!(vad.stop().kind, DecisionKind::None);
    }
    #[test]
    fn continuous_noise_is_learned_but_speech_gaps_protect_a_talker() {
        let mut noise = Vad::new(VadConfig::default());
        feed(&mut noise, 2600, 0.08);
        assert!(noise.noise_floor_db() > -40.0);
        assert!(noise.take_floor_raise().is_some());
        let mut speech = Vad::new(VadConfig::default());
        for _ in 0..20 {
            feed(&mut speech, 300, 0.08);
            feed(&mut speech, 100, 0.0);
        }
        assert_eq!(speech.noise_floor_db(), -60.0);
        assert!(speech.is_active());
    }
    #[test]
    fn echo_return_is_diagnostic_and_once_only() {
        let mut vad = Vad::new(VadConfig::default());
        feed(&mut vad, 300, 0.0);
        vad.set_assistant_speaking(true);
        feed(&mut vad, 100, 0.0);
        vad.set_assistant_speaking(true);
        feed(&mut vad, 20, 0.01);
        let (ms, level) = vad.take_echo_return().unwrap();
        assert_eq!(ms, 100.0);
        assert!(level > -45.0);
        assert!(vad.take_echo_return().is_none());
        assert!(!vad.trace().is_empty());
    }
    #[test]
    fn floor_trace_records_the_audio_clock() {
        let mut vad = Vad::new(VadConfig::default());
        feed(&mut vad, 100, 0.0);
        let s = &vad.trace()[0];
        assert_eq!(s.ms, 0);
        assert_eq!(s.floor, -60.0);
        assert_eq!(s.bar, -50.0);
        assert_eq!(s.voiced_ms, 0);
    }
}
