//! Speaker attribution on a nonblocking side channel. Recognizer audio is never filtered.
use crate::config::Config;
use futures_util::{SinkExt, StreamExt};
use serde::Deserialize;
use serde_json::json;
use std::{
    collections::VecDeque,
    sync::{
        Arc, Mutex,
        atomic::{AtomicI64, Ordering},
    },
    time::Duration,
};
use tokio::sync::mpsc;
use tokio_tungstenite::{
    connect_async_with_config,
    tungstenite::{
        Message, client::IntoClientRequest, http::HeaderValue, protocol::WebSocketConfig,
    },
};
use tokio_util::sync::CancellationToken;

const KEEP_MS: i64 = 90_000;
const MAX_FRAMES: usize = 5000;
const MAX_MASKS: usize = 512;

#[derive(Clone, Debug)]
pub struct Frame {
    pub start_ms: i64,
    pub end_ms: i64,
    pub probabilities: Vec<f32>,
}
#[derive(Clone, Debug, Default)]
pub struct Attribution {
    pub veto: bool,
    /// At least 160 ms of the caller's voice, sufficient to yield on barge-in.
    pub primary: bool,
    pub primary_share: f64,
    pub other_share: f64,
    pub voiced_ms: i64,
    pub coverage: f64,
    pub known: bool,
}

/// The pure tracker mirrors internal/focus, with bounded history and duplicate-label replacement.
pub struct Tracker {
    frames: VecDeque<Frame>,
    masks: VecDeque<(i64, i64)>,
    primary: Option<usize>,
    pinned: bool,
    spoken: Vec<i64>,
    masked_ms: Vec<i64>,
    other_since: Option<(i64, usize)>,
    handovers: usize,
    labelled_to: i64,
    active: f32,
    enroll_ms: i64,
    handover_ms: i64,
}
impl Default for Tracker {
    fn default() -> Self {
        Self {
            frames: VecDeque::new(),
            masks: VecDeque::new(),
            primary: None,
            pinned: false,
            spoken: Vec::new(),
            masked_ms: Vec::new(),
            other_since: None,
            handovers: 0,
            labelled_to: 0,
            active: 0.5,
            enroll_ms: 600,
            handover_ms: 4000,
        }
    }
}
impl Tracker {
    pub fn with_handover(handover_ms: i64) -> Self {
        Self {
            handover_ms,
            ..Default::default()
        }
    }
    pub fn primary(&self) -> Option<usize> {
        self.primary
    }
    pub fn handovers(&self) -> usize {
        self.handovers
    }
    pub fn labelled_to(&self) -> i64 {
        self.labelled_to
    }
    pub fn retained_frames(&self) -> usize {
        self.frames.len()
    }
    pub fn pin(&mut self, speaker: usize) {
        self.primary = Some(speaker);
        self.pinned = true;
    }
    pub fn mask(&mut self, start: i64, end: i64) {
        if end <= start {
            return;
        }
        // Engine updates are chronological; merge overlaps even when its last tail is extended.
        if let Some(last) = self.masks.back_mut().filter(|last| last.1 >= start) {
            last.0 = last.0.min(start);
            last.1 = last.1.max(end);
        } else {
            self.masks.push_back((start, end));
        }
        while self.masks.len() > MAX_MASKS {
            self.masks.pop_front();
        }
    }
    pub fn masked_within(&self, start: i64, end: i64) -> bool {
        self.masks
            .iter()
            .any(|&(from, to)| from < end && to > start)
    }
    fn masked(&self, start: i64, end: i64) -> bool {
        let middle = start + (end - start) / 2;
        self.masks
            .iter()
            .any(|&(from, to)| from <= middle && to > middle)
    }
    pub fn observe(&mut self, frame: Frame) {
        if frame.start_ms < 0
            || frame.end_ms <= frame.start_ms
            || frame.end_ms - frame.start_ms > 1000
            || frame.probabilities.len() > 128
            || frame
                .probabilities
                .iter()
                .any(|p| !p.is_finite() || !(0.0..=1.0).contains(p))
        {
            return;
        }
        if frame.end_ms <= self.labelled_to {
            if let Some(previous) = self
                .frames
                .iter_mut()
                .find(|f| f.start_ms == frame.start_ms && f.end_ms == frame.end_ms)
            {
                *previous = frame;
            }
            return;
        }
        // Partial overlap would inflate coverage and voice duration; only new disjoint labels count.
        if frame.start_ms < self.labelled_to {
            return;
        }
        self.labelled_to = frame.end_ms;
        self.spoken
            .resize(self.spoken.len().max(frame.probabilities.len()), 0);
        self.masked_ms.resize(self.spoken.len(), 0);
        let duration = frame.end_ms - frame.start_ms;
        let masked = self.masked(frame.start_ms, frame.end_ms);
        let mut loudest = None;
        for (speaker, &p) in frame.probabilities.iter().enumerate() {
            if p >= self.active {
                if masked {
                    self.masked_ms[speaker] += duration;
                } else {
                    self.spoken[speaker] += duration;
                }
                if loudest.is_none_or(|(_, old)| p > old) {
                    loudest = Some((speaker, p));
                }
            }
        }
        if !masked && !self.pinned {
            if self.primary.is_none() {
                self.primary = self.spoken.iter().position(|&ms| ms >= self.enroll_ms);
            } else {
                let primary_active = self
                    .primary
                    .and_then(|s| frame.probabilities.get(s))
                    .is_some_and(|p| *p >= self.active);
                if primary_active || loudest.is_none() {
                    self.other_since = None;
                } else if let Some((speaker, _)) = loudest {
                    match self.other_since {
                        Some((start, old))
                            if old == speaker && frame.end_ms - start >= self.handover_ms =>
                        {
                            self.primary = Some(speaker);
                            self.other_since = None;
                            self.handovers += 1;
                        }
                        Some((_, old)) if old == speaker => (),
                        _ => self.other_since = Some((frame.start_ms, speaker)),
                    }
                }
            }
        }
        self.frames.push_back(frame);
        while self
            .frames
            .front()
            .is_some_and(|f| f.end_ms < self.labelled_to - KEEP_MS)
            || self.frames.len() > MAX_FRAMES
        {
            self.frames.pop_front();
        }
        while self
            .masks
            .front()
            .is_some_and(|span| span.1 < self.labelled_to - KEEP_MS)
        {
            self.masks.pop_front();
        }
    }
    pub fn echo(&self) -> Option<usize> {
        self.masked_ms
            .iter()
            .enumerate()
            .filter(|&(speaker, ms)| {
                Some(speaker) != self.primary
                    && *ms >= 800
                    && *ms as f64 / (self.spoken[speaker] + *ms) as f64 >= 0.7
            })
            .max_by_key(|(_, ms)| *ms)
            .map(|(speaker, _)| speaker)
    }
    pub fn attribute(&self, start: i64, end: i64) -> Attribution {
        self.attribute_impl(start, end, false)
    }
    pub fn attribute_over(&self, start: i64, end: i64) -> Attribution {
        self.attribute_impl(start, end, true)
    }
    fn attribute_impl(&self, start: i64, end: i64, through_masks: bool) -> Attribution {
        let mut out = Attribution {
            known: self.primary.is_some(),
            ..Default::default()
        };
        if end <= start {
            return out;
        }
        let echo = if through_masks { self.echo() } else { None };
        let (mut labelled, mut primary, mut others) = (0, 0, 0);
        for f in &self.frames {
            let duration = (f.end_ms.min(end) - f.start_ms.max(start)).max(0);
            if duration == 0 {
                continue;
            }
            labelled += duration;
            if !through_masks && self.masked(f.start_ms, f.end_ms) {
                continue;
            }
            let mut anyone = false;
            let mut caller = false;
            for (speaker, p) in f.probabilities.iter().enumerate() {
                if Some(speaker) != echo && *p >= self.active {
                    anyone = true;
                    if Some(speaker) == self.primary {
                        caller = true;
                    }
                }
            }
            if anyone {
                out.voiced_ms += duration;
                if caller {
                    primary += duration;
                } else {
                    others += duration;
                }
            }
        }
        out.coverage = (labelled as f64 / (end - start) as f64).min(1.0);
        if out.voiced_ms > 0 {
            out.primary_share = primary as f64 / out.voiced_ms as f64;
            out.other_share = others as f64 / out.voiced_ms as f64;
        }
        out.primary = out.known && primary >= 160;
        out
    }
    pub fn spoken_since(&self, since: i64) -> (i64, i64) {
        let (mut caller, mut others) = (0, 0);
        for f in &self.frames {
            if f.start_ms < since || self.masked(f.start_ms, f.end_ms) {
                continue;
            }
            let duration = f.end_ms - f.start_ms;
            let primary = self
                .primary
                .and_then(|s| f.probabilities.get(s))
                .is_some_and(|p| *p >= self.active);
            if primary {
                caller += duration;
            } else if f.probabilities.iter().any(|p| *p >= self.active) {
                others += duration;
            }
        }
        (caller, others)
    }
    fn quiet_since(&self, after: i64) -> Option<i64> {
        let primary = self.primary?;
        let mut cursor = self.labelled_to;
        let mut quiet = None;
        for f in self.frames.iter().rev() {
            if f.end_ms <= after {
                break;
            }
            if f.end_ms < cursor || self.masked(f.start_ms, f.end_ms) {
                return None;
            }
            if f.probabilities
                .get(primary)
                .is_some_and(|p| *p >= self.active)
            {
                return quiet;
            }
            cursor = f.start_ms.max(after);
            quiet = Some(cursor);
            if cursor <= after {
                break;
            }
        }
        // Never force-close an utterance in which the caller has not been heard.
        None
    }
}

#[derive(Default)]
struct State {
    tracker: Tracker,
    connected: bool,
    distrust: bool,
    checked: bool,
    enrolled_at: Option<i64>,
    offered: usize,
}
#[derive(Clone)]
pub struct Focus {
    state: Arc<Mutex<State>>,
    tx: mpsc::Sender<Command>,
    input_to: Arc<AtomicI64>,
    veto_mode: bool,
    veto_share: f64,
    min_voiced: i64,
    profile: String,
}
enum Command {
    Audio { bytes: Vec<u8>, at: i64 },
    Enrol { from: i64, to: i64, profile: String },
}

impl Focus {
    /// Launch without blocking the session actor. Unavailable or failed channels remain fail-open.
    pub fn spawn(cfg: Config, cancel: CancellationToken) -> Self {
        let (state, input_to) = (
            Arc::new(Mutex::new(State::default())),
            Arc::new(AtomicI64::new(0)),
        );
        let (tx, rx) = mpsc::channel(8);
        let mode = cfg.string("duplex.focus", "");
        let url = cfg.string("duplex.focus_url", "");
        let profile = cfg.string("duplex.focus_profile", "");
        let handle = Self {
            state: state.clone(),
            tx,
            input_to: input_to.clone(),
            veto_mode: mode == "veto",
            veto_share: cfg.f64("duplex.focus_veto_share", 0.7).clamp(0.01, 1.0),
            min_voiced: cfg.usize("duplex.focus_min_voiced_ms", 240).max(1) as i64,
            profile: profile.clone(),
        };
        if !mode.is_empty() && !url.is_empty() {
            let token_file = cfg.string("duplex.focus_token_file", "");
            tokio::spawn(async move {
                if run(
                    url,
                    token_file,
                    profile,
                    rx,
                    state.clone(),
                    input_to,
                    cancel,
                )
                .await
                .is_err()
                {
                    tracing::warn!(
                        "focus side channel unavailable; continuing without speaker veto"
                    );
                }
                state.lock().unwrap_or_else(|e| e.into_inner()).connected = false;
            });
        }
        handle
    }
    pub fn push(&self, pcm: &[u8], at_ms: i64) {
        if pcm.is_empty() || pcm.len() % 2 != 0 || at_ms < 0 {
            return;
        }
        self.input_to
            .fetch_max(at_ms + (pcm.len() / 32) as i64, Ordering::Relaxed);
        if !self.is_ready() {
            return;
        }
        let _ = self.tx.try_send(Command::Audio {
            bytes: pcm.to_vec(),
            at: at_ms,
        });
    }
    pub fn mask(&self, start: i64, end: i64) {
        self.state
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .tracker
            .mask(start, end);
    }
    pub fn is_ready(&self) -> bool {
        self.state
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .connected
    }
    pub fn acts(&self) -> bool {
        let state = self.state.lock().unwrap_or_else(|e| e.into_inner());
        self.veto_mode && state.connected && !state.distrust
    }
    pub fn labelled_to(&self) -> i64 {
        self.state
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .tracker
            .labelled_to()
    }
    pub fn attribute(&self, start: i64, end: i64) -> Attribution {
        self.attribute_impl(start, end, false)
    }
    pub fn attribute_over(&self, start: i64, end: i64) -> Attribution {
        self.attribute_impl(start, end, true)
    }
    fn attribute_impl(&self, start: i64, end: i64, over: bool) -> Attribution {
        let state = self.state.lock().unwrap_or_else(|e| e.into_inner());
        if !state.connected {
            return Attribution::default();
        }
        let mut a = if over {
            state.tracker.attribute_over(start, end)
        } else {
            state.tracker.attribute(start, end)
        };
        let coverage = if over { 0.6 } else { 0.8 };
        let other = a.voiced_ms >= self.min_voiced
            && a.other_share >= self.veto_share
            && a.primary_share == 0.0;
        let echo_only = over && a.voiced_ms == 0;
        a.veto = self.veto_mode
            && !state.distrust
            && a.known
            && a.coverage >= coverage
            && (other || echo_only);
        a
    }
    /// Offer only an isolated caller utterance; the engine must require >=600ms actual voice.
    pub fn enrol(&self, start: i64, end: i64) {
        let mut state = self.state.lock().unwrap_or_else(|e| e.into_inner());
        if !state.connected
            || state.offered >= 6
            || end - start < 600
            || state.tracker.masked_within(start, end.saturating_add(600))
        {
            return;
        }
        let a = state.tracker.attribute(start, end);
        if a.known
            && a.coverage >= 0.8
            && a.voiced_ms >= self.min_voiced
            && a.primary_share == 0.0
            && a.other_share >= self.veto_share
        {
            return;
        }
        if self
            .tx
            .try_send(Command::Enrol {
                from: start,
                to: end,
                profile: self.profile.clone(),
            })
            .is_ok()
        {
            state.offered += 1;
        }
    }
    /// Start of contiguous labelled caller silence after a known caller span, on the audio clock.
    pub fn quiet_since(&self, after_ms: i64) -> Option<i64> {
        let state = self.state.lock().unwrap_or_else(|e| e.into_inner());
        if !self.veto_mode
            || !state.connected
            || state.distrust
            || self.input_to.load(Ordering::Relaxed) - state.tracker.labelled_to() > 1000
        {
            return None;
        }
        state.tracker.quiet_since(after_ms)
    }
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct Wire {
    #[serde(rename = "type")]
    kind: String,
    start_ms: i64,
    frame_ms: i64,
    p: Vec<Vec<f32>>,
    profile: String,
    found: Option<bool>,
    rejected: bool,
}

async fn run(
    url: String,
    token_file: String,
    profile: String,
    mut rx: mpsc::Receiver<Command>,
    state: Arc<Mutex<State>>,
    input_to: Arc<AtomicI64>,
    cancel: CancellationToken,
) -> Result<(), ()> {
    let parsed = url::Url::parse(&url).map_err(|_| ())?;
    if !matches!(parsed.scheme(), "ws" | "wss") {
        return Err(());
    }
    let mut request = url.into_client_request().map_err(|_| ())?;
    if !token_file.is_empty() {
        match tokio::fs::read_to_string(token_file).await {
            Ok(token) => {
                let mut header =
                    HeaderValue::from_str(&format!("Bearer {}", token.trim())).map_err(|_| ())?;
                header.set_sensitive(true);
                request.headers_mut().insert("Authorization", header);
            }
            Err(_) => tracing::warn!("focus token file unavailable; connecting without a token"),
        }
    }
    let mut wire_config = WebSocketConfig::default();
    wire_config.max_message_size = Some(256 * 1024);
    wire_config.max_frame_size = Some(256 * 1024);
    let (mut socket, _) = tokio::select! {_ = cancel.cancelled()=>return Ok(()),connection=tokio::time::timeout(Duration::from_secs(5),connect_async_with_config(request, Some(wire_config), false))=>connection.map_err(|_|())?.map_err(|_|())?};
    let first = tokio::select! {_ = cancel.cancelled()=>return Ok(()),first=tokio::time::timeout(Duration::from_secs(15),socket.next())=>first.map_err(|_|())?.ok_or(())?.map_err(|_|())?};
    let first: Wire = serde_json::from_slice(&first.into_data()).map_err(|_| ())?;
    if first.kind != "ready" {
        return Err(());
    }
    state.lock().unwrap_or_else(|e| e.into_inner()).connected = true;
    if !profile.is_empty() {
        let message = Message::Text(json!({"type":"enrol","profile":profile}).to_string().into());
        tokio::select! {_ = cancel.cancelled()=>return Ok(()),sent=tokio::time::timeout(Duration::from_millis(200),socket.send(message))=>sent.map_err(|_|())?.map_err(|_|())?};
    }
    loop {
        tokio::select! {
            _=cancel.cancelled()=>return Ok(()),
            command=rx.recv()=>{
                let Some(command)=command else{return Ok(());};
                let message=match command {
                    Command::Audio{bytes,at}=>{
                        if input_to.load(Ordering::Relaxed)-at>300{continue;}
                        let mut data=Vec::with_capacity(bytes.len()+8);data.extend_from_slice(&at.to_le_bytes());data.extend(bytes);Message::Binary(data.into())
                    },
                    Command::Enrol{from,to,profile}=>Message::Text(json!({"type":"enrol","from_ms":from,"to_ms":to,"save_as":profile}).to_string().into()),
                };
                tokio::select!{_ = cancel.cancelled()=>return Ok(()),sent=tokio::time::timeout(Duration::from_millis(200),socket.send(message))=>sent.map_err(|_|())?.map_err(|_|())?};
            },
            message=socket.next()=>{
                let Some(Ok(message))=message else{return Err(());};
                match message {Message::Ping(bytes)=>{tokio::select! {_ = cancel.cancelled()=>return Ok(()),sent=tokio::time::timeout(Duration::from_millis(200),socket.send(Message::Pong(bytes)))=>sent.map_err(|_|())?.map_err(|_|())?};continue;},Message::Close(_)=>return Ok(()),Message::Text(_)|Message::Binary(_)=>(),_=>continue}
                let data=message.into_data();if data.len()>256*1024{return Err(());}
                let Ok(message)=serde_json::from_slice::<Wire>(&data)else{continue;};
                let mut state=state.lock().unwrap_or_else(|e|e.into_inner());
                match message.kind.as_str() {
                    "enrolled" if !message.rejected&&(message.profile.is_empty()||message.found.unwrap_or(true))=>{
                        if state.enrolled_at.is_none(){state.enrolled_at=Some(input_to.load(Ordering::Relaxed));state.tracker.pin(0);}
                    },
                    "frames" if (1..=1000).contains(&message.frame_ms)&&message.p.len()<=1024=>{
                        for(index,probabilities)in message.p.into_iter().enumerate(){
                            let Some(start)=message.start_ms.checked_add(index as i64*message.frame_ms)else{continue;};
                            let Some(end)=start.checked_add(message.frame_ms)else{continue;};
                            if end>input_to.load(Ordering::Relaxed)+1000{continue;}
                            state.tracker.observe(Frame{start_ms:start,end_ms:end,probabilities});
                        }
                        if let Some(since)=state.enrolled_at.filter(|_|!state.checked){
                            let(caller,others)=state.tracker.spoken_since(since);
                            if caller+others>=8000{state.checked=true;if caller as f64/((caller+others)as f64)<0.3{state.distrust=true;tracing::warn!("focus enrollment contradicted by subsequent voice; observing only");}}
                        }
                    },
                    "error"=>tracing::warn!("focus side channel reported an error"),
                    _=>(),
                }
            },
        }
    }
}
