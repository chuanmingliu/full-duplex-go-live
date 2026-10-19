//! One actor owns each conversation. Network workers communicate through bounded
//! queues; generation IDs prevent a cancelled answer from leaking onto the wire.
mod generation;
mod history;
mod listen;
mod tools;

use crate::{
    audio,
    config::Config,
    endpoint,
    provider::{self, AsrCommand, Message},
    vad::{DecisionKind, Vad, VadConfig},
};
use serde_json::{Value, json};
use std::{collections::BTreeMap, time::Duration};
use tokio::{
    sync::{mpsc, watch},
    time::Instant,
};
use tokio_util::sync::CancellationToken;
use uuid::Uuid;

pub enum EngineCommand {
    Audio(Vec<u8>),
    UserText(String),
    Generate,
    Cancel,
    Mute(bool),
    AppendInstructions(String),
    AppendThinking(String),
    Speak(String),
    Commentary(String),
    Update(Config),
    ToolResult { id: String, output: String },
}

pub struct EngineHandle {
    pub commands: mpsc::Sender<EngineCommand>,
    pub events: mpsc::Receiver<Value>,
    pub cancel: CancellationToken,
}

pub fn spawn(mut config: Config) -> EngineHandle {
    if config.value("_server_tools").is_none() {
        config.set(
            "_server_tools",
            config.value("tools").cloned().unwrap_or(json!([])),
        );
    }
    let (commands, rx) = mpsc::channel(128);
    let (events, output) = mpsc::channel(128);
    let cancel = CancellationToken::new();
    let warm_config = config.clone();
    let warm_cancel = cancel.child_token();
    tokio::spawn(async move {
        let _ = provider::prewarm(warm_config, warm_cancel).await;
    });
    let engine = Engine::new(config, events, cancel.clone());
    tokio::spawn(engine.run(rx));
    EngineHandle {
        commands,
        events: output,
        cancel,
    }
}

/// Prepare an opening line in the same cache used by session playback.
pub async fn warm_speech(
    config: Config,
    text: String,
    cancel: CancellationToken,
) -> anyhow::Result<usize> {
    let cache = tools::cache_for(&config);
    if let Some(pcm) = cache.lock().await.get(&text) {
        return Ok(pcm.len());
    }
    let tts = provider::Synthesizer::new(config);
    let mut audio = tts.synthesize(text.clone(), cancel.clone()).await?;
    let mut pcm = Vec::new();
    while let Some(chunk) = tokio::select! {_=cancel.cancelled()=>anyhow::bail!("opening cancelled"),chunk=audio.recv()=>chunk}
    {
        let chunk = chunk?;
        if pcm.len() + chunk.len() > 1_920_000 {
            anyhow::bail!("opening exceeds sixty seconds");
        }
        pcm.extend_from_slice(&chunk);
    }
    if pcm.is_empty() {
        anyhow::bail!("synthesizer returned no opening audio");
    }
    let len = pcm.len();
    let mut cache = cache.lock().await;
    if cache.len() >= 128 {
        cache.clear();
    }
    cache.insert(text, pcm);
    Ok(len)
}

pub(super) enum Internal {
    Recognized {
        utterance: u64,
        segment: u64,
        text: String,
        final_result: bool,
    },
    RecognitionEnded {
        utterance: u64,
        segment: u64,
        error: Option<String>,
    },
    Output {
        generation: u64,
        event: Value,
    },
    Spoken {
        generation: u64,
        text: String,
        start_ms: i64,
        end_ms: i64,
    },
    Completed {
        generation: u64,
    },
    ToolHistory {
        generation: u64,
        messages: Vec<Message>,
    },
    ToolDelivery {
        name: String,
        id: String,
        version: u64,
        output: Value,
    },
}

#[derive(Clone, Copy, Default)]
pub(super) struct Permit {
    pub confirmed: bool,
    pub speech_end_ms: i64,
    pub silence_ms: i64,
    pub input_start_ms: i64,
    pub input_end_ms: i64,
}

struct Recognition {
    text: String,
    final_result: bool,
    commands: mpsc::Sender<AsrCommand>,
    cancel: CancellationToken,
}
struct Listening {
    id: u64,
    start_ms: i64,
    end_ms: i64,
    silence_ms: i64,
    segments: BTreeMap<u64, Recognition>,
    current: Option<u64>,
    stopped: Option<Instant>,
    barge_in: bool,
    yielded: bool,
    began: Instant,
    active_ms: f64,
}
impl Listening {
    fn text(&self) -> String {
        self.segments
            .values()
            .map(|s| s.text.as_str())
            .collect::<Vec<_>>()
            .join("")
    }
    fn complete(&self) -> bool {
        self.segments.values().all(|s| s.final_result)
    }
    fn cancel(&self) {
        for s in self.segments.values() {
            s.cancel.cancel();
        }
    }
}
struct Generation {
    id: u64,
    item_id: String,
    cancel: CancellationToken,
    permit: watch::Sender<Permit>,
    transcript: String,
    spoken: String,
    audio_ms: i64,
    confirmed: bool,
}
struct Engine {
    cfg: Config,
    events: mpsc::Sender<Value>,
    cancel: CancellationToken,
    inner: mpsc::Sender<Internal>,
    incoming: mpsc::Receiver<Internal>,
    vad: Vad,
    framer: audio::Framer,
    listening: Option<Listening>,
    generation: Option<Generation>,
    history: Vec<Message>,
    next_id: u64,
    epoch: Instant,
    muted: bool,
    last_audio: Option<Instant>,
    last_activity: Instant,
    output_speaking: bool,
    tts: provider::Synthesizer,
    tools: tools::Runtime,
    deliveries: Vec<(String, String, u64, Value)>,
    commentary: std::collections::VecDeque<String>,
    focus: crate::focus::Focus,
    pending_response: Option<Permit>,
}

fn message(role: &str, content: String) -> Message {
    Message {
        role: role.into(),
        content,
        tool_call_id: None,
        tool_calls: vec![],
    }
}

impl Engine {
    fn new(cfg: Config, events: mpsc::Sender<Value>, cancel: CancellationToken) -> Self {
        let mut vcfg: VadConfig =
            serde_json::from_value(cfg.value("vad").cloned().unwrap_or(json!({})))
                .unwrap_or_default();
        vcfg.sample_rate = 16000;
        let frame_bytes = cfg.usize("vad.frame_ms", 20) * 32;
        let (inner, incoming) = mpsc::channel(128);
        let tts = provider::Synthesizer::new(cfg.clone());
        let tools = tools::Runtime::new(cfg.clone(), inner.clone(), cancel.clone());
        let focus = crate::focus::Focus::spawn(cfg.clone(), cancel.clone());
        let history = cfg
            .value("session_input")
            .cloned()
            .and_then(|v| serde_json::from_value::<Vec<Message>>(v).ok())
            .unwrap_or_default();
        Self {
            cfg,
            events,
            cancel,
            inner,
            incoming,
            vad: Vad::new(vcfg),
            framer: audio::Framer::new(frame_bytes).expect("validated nonzero frame size"),
            listening: None,
            generation: None,
            history,
            next_id: 0,
            epoch: Instant::now(),
            muted: false,
            last_audio: None,
            last_activity: Instant::now(),
            output_speaking: false,
            tts,
            tools,
            deliveries: vec![],
            commentary: std::collections::VecDeque::new(),
            focus,
            pending_response: None,
        }
    }
    fn now_ms(&self) -> i64 {
        self.epoch.elapsed().as_millis() as i64
    }
    fn emit(&self, mut event: Value) {
        if event.get("event_id").is_none() {
            event["event_id"] = json!(format!("evt_{}", Uuid::new_v4().simple()));
        }
        // A slow client cannot consume unbounded memory or keep audio queued
        // behind an interruption. Closing the session is preferable to stale speech.
        if self.events.try_send(event).is_err() {
            self.cancel.cancel();
        }
    }
    fn error(&self, code: &str, message: &str) {
        self.emit(
            json!({"type":"error","error":{"type":"server_error","code":code,"message":message}}),
        );
    }
    async fn run(mut self, mut commands: mpsc::Receiver<EngineCommand>) {
        let mut tick = tokio::time::interval(Duration::from_millis(20));
        tick.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);
        loop {
            tokio::select! {
                biased;
                _=self.cancel.cancelled()=>break,
                command=commands.recv()=>match command { Some(c)=>self.command(c),None=>break },
                event=self.incoming.recv()=>if let Some(event)=event { self.internal(event); },
                _=tick.tick()=>self.tick(),
            }
            tokio::task::yield_now().await;
        }
        self.cancel.cancel();
        if let Some(listen) = &self.listening {
            listen.cancel();
        }
        if let Some(generation) = &self.generation {
            generation.cancel.cancel();
        }
    }
    fn command(&mut self, command: EngineCommand) {
        self.last_activity = Instant::now();
        match command {
            EngineCommand::Audio(pcm) => {
                self.last_audio = Some(Instant::now());
                if pcm.len() % 2 != 0 {
                    self.error("invalid_audio", "PCM16 input must have an even byte length");
                    return;
                }
                if !self.muted {
                    for frame in self.framer.push(&pcm) {
                        self.frame(&frame);
                    }
                }
            }
            EngineCommand::UserText(text) => {
                let text = text.trim().to_owned();
                if text.is_empty() {
                    return;
                }
                self.cancel_generation(true);
                self.resolve_abandoned_tools();
                self.history.push(message("user", text));
                self.trim_history();
            }
            EngineCommand::Generate => {
                if self.pending_tool_ids().is_empty() {
                    self.generate(
                        None,
                        true,
                        None,
                        Permit {
                            confirmed: true,
                            speech_end_ms: self.now_ms(),
                            silence_ms: 0,
                            ..Permit::default()
                        },
                    );
                } else {
                    self.error(
                        "pending_tool_results",
                        "provide all pending function results before response.create",
                    );
                }
            }
            EngineCommand::Cancel => self.cancel_generation(true),
            EngineCommand::Mute(mute) => {
                self.muted = mute;
                if mute {
                    if let Some(l) = self.listening.take() {
                        l.cancel();
                    }
                    self.vad.reset();
                }
            }
            EngineCommand::AppendInstructions(text) => {
                let limit = self.cfg.usize("duplex.max_append_chars", 1500);
                if text.chars().count() > limit {
                    self.error("append_too_long", "instructions exceed max_append_chars");
                    return;
                }
                let old = self.cfg.string("instructions", "");
                if old.len() + text.len() + 1 > 256 * 1024 {
                    self.error(
                        "instructions_full",
                        "accumulated instructions exceed 256 KiB",
                    );
                    return;
                }
                self.cfg
                    .set("instructions", json!(format!("{old}\n{text}")));
            }
            EngineCommand::AppendThinking(text) => {
                if text.chars().count() > self.cfg.usize("duplex.max_append_chars", 1500) {
                    self.error("append_too_long", "thinking exceeds max_append_chars");
                    return;
                }
                self.history.push(message("system", text));
                self.trim_history();
            }
            EngineCommand::Speak(text) => {
                if !text.trim().is_empty() {
                    self.generate(
                        None,
                        true,
                        Some(text),
                        Permit {
                            confirmed: true,
                            speech_end_ms: self.now_ms(),
                            silence_ms: 0,
                            ..Permit::default()
                        },
                    );
                }
            }
            EngineCommand::Commentary(text) => {
                if self.commentary.len() >= 32 {
                    self.error("commentary_overflow", "commentary queue is full");
                } else {
                    self.commentary.push_back(text);
                }
            }
            EngineCommand::Update(cfg) => {
                self.tools.update_config(cfg.clone());
                self.cfg = cfg;
                self.vad
                    .set_min_silence_ms(self.cfg.usize("vad.min_silence_ms", 380) as i32);
                self.tts = provider::Synthesizer::new(self.cfg.clone());
            }
            EngineCommand::ToolResult { id, output } => {
                // A standalone tool result without a matching assistant tool call
                // is invalid chat-completions history. Reject it explicitly.
                if self.pending_tool_ids().contains(&id) {
                    self.insert_tool_result(id, output);
                } else {
                    self.error(
                        "unknown_tool_call",
                        "function result does not match a pending tool call",
                    );
                }
            }
        }
    }
    fn internal(&mut self, event: Internal) {
        match event {
            Internal::Recognized {
                utterance,
                segment,
                text,
                final_result,
            } => {
                let Some(l) = &mut self.listening else {
                    return;
                };
                if l.id != utterance {
                    return;
                }
                if let Some(r) = l.segments.get_mut(&segment) {
                    r.text = text;
                    r.final_result = final_result;
                }
                let text = l.text();
                let start = l.start_ms;
                let barge = l.barge_in;
                let stopped = l.stopped.is_some();
                self.emit(json!({"type":"golive.input_transcript.updated","item_id":format!("user_{utterance}"),"text":text,"start_ms":start,"end_ms":self.now_ms(),"final":false}));
                // Ignore recognizer-added final punctuation when deciding whether
                // the caller is mid-thought (Tencent finishes fragments with 。).
                let semantic = text.trim_end_matches(['。', '，', '.', '!', '！', '?', '？']);
                let patient = self.cfg.bool("duplex.semantic_endpointing", true)
                    && endpoint::classify(semantic) == endpoint::Class::Incomplete;
                let silence = if patient {
                    self.cfg
                        .usize("duplex.patient_silence_ms", 700)
                        .max(self.cfg.usize("vad.min_silence_ms", 380))
                } else {
                    self.cfg.usize("vad.min_silence_ms", 380)
                };
                self.vad.set_min_silence_ms(silence as i32);
                if barge {
                    self.maybe_yield();
                }
                if !stopped
                    && final_result
                    && self.cfg.bool("duplex.speculative", true)
                    && !text.trim().is_empty()
                    && !self.backchannel_candidate(&text)
                    && (!patient || self.cfg.bool("duplex.speculate_while_patient", false))
                    && !self.output_speaking
                {
                    let same = self.generation.as_ref().is_some_and(|g| {
                        endpoint::normalize(&g.transcript) == endpoint::normalize(&text)
                    });
                    if !same {
                        self.generate(Some(text), false, None, Permit::default());
                    }
                }
                self.finish_utterance_if_ready(false);
            }
            Internal::RecognitionEnded {
                utterance,
                segment,
                error,
            } => {
                if let Some(l) = &mut self.listening {
                    if l.id == utterance {
                        if let Some(r) = l.segments.get_mut(&segment) {
                            r.final_result = true;
                        }
                    }
                }
                if error.is_some() && self.listening.as_ref().is_some_and(|l| l.id == utterance) {
                    self.error(
                        "asr_failed",
                        "recognition failed; check provider connectivity and configuration",
                    );
                }
                self.finish_utterance_if_ready(false);
            }
            Internal::Output { generation, event } => {
                if self.generation.as_ref().is_some_and(|g| g.id == generation) {
                    if event["type"] == "session.output_audio.delta" {
                        self.output_speaking = true;
                        self.vad.set_assistant_speaking(true);
                        let audio_at = self.vad.clock_ms() as i64;
                        let duration = event["end_ms"].as_i64().unwrap_or(0)
                            - event["start_ms"].as_i64().unwrap_or(0);
                        self.focus.mask(
                            audio_at,
                            audio_at + duration + self.cfg.usize("vad.echo_tail_ms", 600) as i64,
                        );
                        if let Some(g) = &mut self.generation {
                            g.audio_ms += event["end_ms"].as_i64().unwrap_or(0)
                                - event["start_ms"].as_i64().unwrap_or(0);
                        }
                    }
                    if event["type"] == "golive.focus" && event["veto"] == true && self.focus.acts()
                    {
                        self.cancel_generation(true);
                    }
                    self.emit(event);
                }
            }
            Internal::Spoken {
                generation,
                text,
                start_ms,
                end_ms,
            } => {
                let item = if let Some(g) = &mut self.generation {
                    if g.id == generation {
                        g.spoken.push_str(&text);
                        Some(g.item_id.clone())
                    } else {
                        None
                    }
                } else {
                    None
                };
                if let Some(item_id) = item {
                    self.emit(json!({"type":"session.output_transcript.delta","item_id":item_id,"delta":text,"start_ms":start_ms,"end_ms":end_ms}));
                }
            }
            Internal::ToolHistory {
                generation,
                messages,
            } => {
                if let Some(g) = &mut self.generation {
                    if g.id == generation {
                        self.history.extend(messages);
                        g.spoken.clear();
                    }
                }
            }
            Internal::Completed { generation } => {
                if self.generation.as_ref().is_some_and(|g| g.id == generation) {
                    if let Some(g) = self.generation.take() {
                        if !g.spoken.is_empty() {
                            self.history.push(message("assistant", g.spoken));
                        }
                    }
                    self.output_speaking = false;
                    self.vad.set_assistant_speaking(false);
                    self.trim_history();
                }
            }
            Internal::ToolDelivery {
                name,
                id,
                version,
                output,
            } => self.deliveries.push((name, id, version, output)),
        }
    }
    fn maybe_yield(&mut self) {
        let Some(l) = &self.listening else {
            return;
        };
        if !l.barge_in
            || l.yielded
            || !self.cfg.bool("duplex.allow_barge_in", true)
            || self.cfg.string("duplex.on_new_query", "cut") != "cut"
        {
            return;
        }
        let text = l.text();
        if text.trim().is_empty() || self.backchannel_candidate(&text) {
            return;
        }
        if self.focus.acts() {
            let attribution = self
                .focus
                .attribute_over(l.start_ms, self.vad.clock_ms() as i64);
            if attribution.veto {
                return;
            }
            let wait = self.cfg.usize("duplex.focus_barge_in_wait_ms", 900);
            if !attribution.primary
                && (!attribution.known || attribution.coverage < 0.6)
                && l.began.elapsed() < Duration::from_millis(wait as u64)
            {
                return;
            }
        }
        self.cancel_generation(true);
        if let Some(l) = &mut self.listening {
            l.yielded = true;
        }
    }
    fn backchannel_candidate(&self, text: &str) -> bool {
        let norm = endpoint::normalize(text);
        if norm.is_empty() {
            return true;
        }
        self.cfg
            .value("duplex.user_backchannel_phrases")
            .and_then(Value::as_array)
            .is_some_and(|values| {
                values
                    .iter()
                    .filter_map(Value::as_str)
                    .any(|p| endpoint::normalize(p).starts_with(&norm))
            })
    }
    fn finish_utterance_if_ready(&mut self, force: bool) {
        let ready = self
            .listening
            .as_ref()
            .is_some_and(|l| l.stopped.is_some() && (force || l.complete()));
        if !ready {
            return;
        }
        let listen = self.listening.take().unwrap();
        listen.cancel();
        let text = listen.text().trim().to_owned();
        self.vad
            .set_min_silence_ms(self.cfg.usize("vad.min_silence_ms", 380) as i32);
        if text.is_empty()
            || (self.cfg.bool("duplex.ignore_hesitations", true) && endpoint::hesitation(&text))
        {
            if self.generation.as_ref().is_some_and(|g| !g.confirmed) {
                self.cancel_generation(false);
            }
            return;
        }
        self.emit(json!({"type":"session.input_transcript.delta","item_id":format!("user_{}",listen.id),"delta":text,"start_ms":listen.start_ms,"end_ms":listen.end_ms,"final":true}));
        self.emit(json!({"type":"golive.input_transcript.updated","item_id":format!("user_{}",listen.id),"text":text,"start_ms":listen.start_ms,"end_ms":listen.end_ms,"final":true}));
        if listen.barge_in && !listen.yielded && self.backchannel_candidate(&text) {
            self.emit(json!({"type":"golive.input_backchannel","text":text}));
            return;
        }
        let attribution = if listen.barge_in {
            self.focus.attribute_over(listen.start_ms, listen.end_ms)
        } else {
            self.focus.attribute(listen.start_ms, listen.end_ms)
        };
        self.emit(json!({"type":"golive.focus","veto":attribution.veto,"primary":attribution.primary,"coverage":attribution.coverage}));
        if attribution.veto && self.focus.acts() {
            if self.generation.as_ref().is_some_and(|g| !g.confirmed) {
                self.cancel_generation(false);
            }
            return;
        }
        if !listen.barge_in && listen.active_ms >= 600.0 {
            self.focus.enrol(listen.start_ms, listen.end_ms);
        }
        self.resolve_abandoned_tools();
        self.history.push(message("user", text.clone()));
        self.trim_history();
        let permit = Permit {
            confirmed: true,
            speech_end_ms: self.now_ms()
                - listen
                    .stopped
                    .map(|t| t.elapsed().as_millis() as i64)
                    .unwrap_or(0),
            silence_ms: listen.silence_ms,
            input_start_ms: listen.start_ms,
            input_end_ms: listen.end_ms,
        };
        if self.output_speaking
            && (!self.cfg.bool("duplex.allow_barge_in", true)
                || self.cfg.string("duplex.on_new_query", "cut") != "cut")
        {
            self.pending_response = Some(permit);
            return;
        }
        let same = self.generation.as_ref().is_some_and(|g| {
            !g.confirmed && endpoint::normalize(&g.transcript) == endpoint::normalize(&text)
        });
        if same {
            if let Some(g) = &mut self.generation {
                g.confirmed = true;
                g.permit.send_replace(permit);
            }
            self.delegation(&text, "final_transcript");
        } else {
            self.generate(None, true, None, permit);
        }
    }
    fn delegation(&self, text: &str, reason: &str) {
        let target = if self.cfg.string("duplex.delegation_mode", "responses") == "client" {
            "client"
        } else {
            "responses"
        };
        self.emit(json!({"type":"session.delegation.created","offset_ms":self.now_ms(),"delegation":{"id":format!("delegation_{}",self.next_id),"type":"delegation","target":target,"reason":reason,"transcript":text}}));
    }
    fn generate(
        &mut self,
        text: Option<String>,
        confirmed: bool,
        speak: Option<String>,
        permit: Permit,
    ) {
        if speak.is_none() && !self.pending_tool_ids().is_empty() {
            return;
        }
        self.cancel_generation(true);
        let transcript = text
            .clone()
            .or_else(|| {
                self.history
                    .iter()
                    .rev()
                    .find(|m| m.role == "user")
                    .map(|m| m.content.clone())
            })
            .unwrap_or_default();
        if confirmed && speak.is_none() {
            self.delegation(&transcript, "final_transcript");
        }
        if speak.is_none() && self.cfg.string("duplex.delegation_mode", "responses") == "client" {
            return;
        }
        let mut messages = vec![message(
            "system",
            self.cfg.string("instructions", "")
                + "\n"
                + &self.cfg.string("backend_instructions", ""),
        )];
        messages.extend(self.history.clone());
        if let Some(text) = text {
            messages.push(message("user", text));
        }
        self.next_id += 1;
        let id = self.next_id;
        let item_id = format!("assistant_{}", Uuid::new_v4().simple());
        let cancel = self.cancel.child_token();
        let (tx, rx) = watch::channel(permit);
        self.generation = Some(Generation {
            id,
            item_id: item_id.clone(),
            cancel: cancel.clone(),
            permit: tx,
            transcript,
            spoken: String::new(),
            audio_ms: 0,
            confirmed,
        });
        let worker = generation::Response {
            id,
            item_id,
            cfg: self.cfg.clone(),
            messages,
            speak,
            tts: self.tts.clone(),
            tools: self.tools.clone(),
            focus: self.focus.clone(),
            cancel,
            permit: rx,
            inner: self.inner.clone(),
            epoch: self.epoch,
        };
        tokio::spawn(worker.run());
    }
    fn cancel_generation(&mut self, notify: bool) {
        if let Some(g) = self.generation.take() {
            g.cancel.cancel();
            if !g.spoken.is_empty() {
                self.history.push(message("assistant", g.spoken.clone()));
            }
            if notify && g.audio_ms > 0 {
                self.emit(json!({"type":"golive.output_audio.truncated","item_id":g.item_id,"played_ms":g.audio_ms,"total_ms":g.audio_ms,"text":g.spoken}));
            }
        }
        self.output_speaking = false;
        self.vad.set_assistant_speaking(false);
    }
    fn tick(&mut self) {
        self.maybe_yield();
        if self.pending_response.is_some()
            && self.listening.is_none()
            && self.generation.is_none()
            && self.pending_tool_ids().is_empty()
        {
            let permit = self.pending_response.take().unwrap();
            self.generate(None, true, None, permit);
        }
        if let Some(l) = &self.listening {
            let quiet = self.cfg.usize("duplex.focus_quiet_close_ms", 0);
            if quiet > 0
                && l.stopped.is_none()
                && self.focus.acts()
                && self
                    .focus
                    .quiet_since(l.start_ms)
                    .is_some_and(|since| self.focus.labelled_to() - since >= quiet as i64)
            {
                let d = self.vad.stop();
                if let Some(l) = &mut self.listening {
                    l.end_ms = d.end_ms;
                    l.silence_ms = d.silence_ms as i64;
                    l.stopped = Some(Instant::now());
                }
                self.finish_recognition();
                self.emit(json!({"type":"golive.speech.stopped","start_ms":d.start_ms,"end_ms":d.end_ms,"silence_ms":d.silence_ms as i64}));
                self.finish_utterance_if_ready(false);
            }
        }
        if self.listening.is_none() && self.generation.is_none() {
            if let Some(text) = self.commentary.pop_front() {
                self.generate(
                    None,
                    true,
                    Some(text),
                    Permit {
                        confirmed: true,
                        speech_end_ms: self.now_ms(),
                        ..Permit::default()
                    },
                );
            }
        }
        let line_fill = self.cfg.usize("vad.line_fill_ms", 100);
        if !self.muted
            && line_fill > 0
            && self
                .last_audio
                .is_some_and(|t| t.elapsed() >= Duration::from_millis(line_fill as u64))
        {
            let silence = vec![0; self.cfg.usize("vad.frame_ms", 20) * 32];
            self.frame(&silence);
        }
        if self
            .listening
            .as_ref()
            .and_then(|l| l.stopped)
            .is_some_and(|t| t.elapsed() > Duration::from_secs(3))
        {
            self.finish_utterance_if_ready(true);
        }
        if self.listening.is_none()
            && self.generation.is_none()
            && !self.deliveries.is_empty()
            && self.pending_tool_ids().is_empty()
        {
            let (name, id, version, output) = self.deliveries.remove(0);
            self.history.push(message("system",format!("Background task {name} ({id}) completed on memory version {version}: {output}. Report this result, considering any newer plan.")));
            self.generate(
                None,
                true,
                None,
                Permit {
                    confirmed: true,
                    speech_end_ms: self.now_ms(),
                    silence_ms: 0,
                    ..Permit::default()
                },
            );
        }
        let max = self.cfg.usize("duplex.session_max_seconds", 3600);
        let idle = self.cfg.usize("duplex.idle_timeout_seconds", 300);
        if (max > 0 && self.epoch.elapsed().as_secs() >= max as u64)
            || (idle > 0 && self.last_activity.elapsed().as_secs() >= idle as u64)
        {
            self.cancel.cancel();
        }
    }
}
