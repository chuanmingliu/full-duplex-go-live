//! Bounded, asynchronous latency-console reporting. Voice work never awaits HTTP.
use crate::config::Config;
use serde_json::{Value, json};
use std::{
    collections::HashMap,
    sync::{
        Arc, Mutex, OnceLock, Weak,
        atomic::{AtomicU64, Ordering},
    },
    time::{Duration, Instant, SystemTime, UNIX_EPOCH},
};
use tokio::sync::mpsc;
use tokio_util::sync::CancellationToken;

const CAPACITY: usize = 512;
const BATCH_SIZE: usize = 64;
const POST_TIMEOUT: Duration = Duration::from_secs(2);

#[derive(Default)]
struct Counters {
    sent: AtomicU64,
    failed: AtomicU64,
    dropped: AtomicU64,
}

struct Poster {
    sender: mpsc::Sender<Value>,
    counters: Arc<Counters>,
    sequence: AtomicU64,
    clock_id: String,
    run_id: String,
    legacy: bool,
    shutdown: CancellationToken,
}

type Registry = Mutex<HashMap<String, Weak<Poster>>>;
static POSTERS: OnceLock<Registry> = OnceLock::new();

fn poster(config: &Config, shutdown: CancellationToken) -> Option<Arc<Poster>> {
    let endpoint = config.string("telemetry_url", "");
    if endpoint.is_empty() {
        return None;
    }
    let format = config.string("telemetry_format", "v1");
    let key = format!("{format}:{endpoint}");
    let mut registry = POSTERS
        .get_or_init(|| Mutex::new(HashMap::new()))
        .lock()
        .unwrap_or_else(|p| p.into_inner());
    if let Some(existing) = registry
        .get(&key)
        .and_then(Weak::upgrade)
        .filter(|p| !p.shutdown.is_cancelled())
    {
        return Some(existing);
    }
    registry.retain(|_, value| value.strong_count() > 0);
    let (sender, receiver) = mpsc::channel(CAPACITY);
    let counters = Arc::new(Counters::default());
    let poster = Arc::new(Poster {
        sender,
        counters: counters.clone(),
        sequence: AtomicU64::new(0),
        clock_id: host_clock_id(),
        run_id: format!("r{}", uuid::Uuid::new_v4().simple()),
        legacy: format == "legacy",
        shutdown: shutdown.clone(),
    });
    registry.insert(key, Arc::downgrade(&poster));
    tokio::spawn(deliver(endpoint, receiver, counters, shutdown));
    Some(poster)
}

async fn deliver(
    endpoint: String,
    mut receiver: mpsc::Receiver<Value>,
    counters: Arc<Counters>,
    shutdown: CancellationToken,
) {
    let client = match reqwest::Client::builder().timeout(POST_TIMEOUT).build() {
        Ok(client) => client,
        Err(_) => {
            tracing::warn!("telemetry HTTP client could not initialize");
            return;
        }
    };
    let mut drain_deadline = None;
    loop {
        // During shutdown the final lifecycle events may still arrive. Give
        // them at most two seconds, including any in-flight HTTP request.
        if shutdown.is_cancelled() && drain_deadline.is_none() {
            drain_deadline = Some(tokio::time::Instant::now() + POST_TIMEOUT);
        }
        let next = async {
            match drain_deadline {
                Some(deadline) => tokio::time::timeout_at(deadline, receiver.recv())
                    .await
                    .ok()
                    .flatten(),
                None => tokio::select! {
                    value=receiver.recv()=>value,
                    _=shutdown.cancelled()=>None,
                },
            }
        }
        .await;
        let Some(first) = next else {
            if shutdown.is_cancelled() && drain_deadline.is_none() {
                continue;
            }
            counters
                .dropped
                .fetch_add(receiver.len() as u64, Ordering::Relaxed);
            return;
        };
        let mut batch = Vec::with_capacity(BATCH_SIZE);
        batch.push(first);
        while batch.len() < BATCH_SIZE {
            match receiver.try_recv() {
                Ok(event) => batch.push(event),
                Err(_) => break,
            }
        }
        let count = batch.len() as u64;
        let request = client.post(&endpoint).json(&batch).send();
        tokio::pin!(request);
        let result = if let Some(deadline) = drain_deadline {
            tokio::time::timeout_at(deadline, &mut request)
                .await
                .ok()
                .and_then(Result::ok)
        } else {
            tokio::select! {
                response=&mut request=>response.ok(),
                _=shutdown.cancelled()=>{
                    let deadline=tokio::time::Instant::now()+POST_TIMEOUT;
                    drain_deadline=Some(deadline);
                    tokio::time::timeout_at(deadline,&mut request).await.ok().and_then(Result::ok)
                },
            }
        };
        match result {
            Some(response) if response.status().as_u16() < 300 => {
                counters.sent.fetch_add(count, Ordering::Relaxed);
            }
            _ => {
                if counters.failed.fetch_add(count, Ordering::Relaxed) == 0 {
                    tracing::warn!(
                        "telemetry console unavailable or refused events; reporting continues without blocking voice"
                    );
                }
            }
        }
    }
}

fn wall_ms() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_millis() as u64
}

#[cfg(target_os = "linux")]
fn host_clock_id() -> String {
    std::fs::read_to_string("/proc/sys/kernel/random/boot_id")
        .ok()
        .filter(|s| !s.trim().is_empty())
        .map(|s| format!("boot:{}", s.trim()))
        .unwrap_or_else(|| "golive:unknown".into())
}
#[cfg(not(target_os = "linux"))]
fn host_clock_id() -> String {
    "golive:local".into()
}

#[cfg(target_os = "linux")]
fn monotonic_ns() -> Option<String> {
    let stamp = rustix::time::clock_gettime(rustix::time::ClockId::Monotonic);
    Some((i128::from(stamp.tv_sec) * 1_000_000_000 + i128::from(stamp.tv_nsec)).to_string())
}
#[cfg(not(target_os = "linux"))]
fn monotonic_ns() -> Option<String> {
    None
}

impl Poster {
    #[allow(clippy::too_many_arguments)] // Wire contract keeps each field explicit at the mapping boundary.
    fn post(
        &self,
        point: &str,
        event: &str,
        component: &str,
        chain: &str,
        turn: u64,
        call: &str,
        session: &str,
        elapsed_ms: u128,
        extra: Value,
    ) {
        let sequence = self.sequence.fetch_add(1, Ordering::Relaxed) + 1;
        let wall = wall_ms();
        let body = if self.legacy {
            let mut extra = extra.as_object().cloned().unwrap_or_default();
            extra.insert("brain".into(), json!("golive"));
            extra.insert("session".into(), json!(session));
            if !call.is_empty() {
                extra.insert("call".into(), json!(call));
            }
            json!({"at":wall,"tMs":elapsed_ms as u64,"component":if component=="phone" {"s2s"}else{component},"event":event,"extra":extra})
        } else {
            if call.is_empty() {
                return;
            }
            json!({"schemaVersion":1,
                "eventId":format!("{}:golive-{}-{}:{}",self.clock_id,component,self.run_id,sequence),
                "callId":call,"turnId":if turn>0{Some(turn)}else{None},"backend":"local","chain":chain,
                "point":point,"component":component,"sequence":sequence,"wallTimeMs":wall,
                "monotonicNs":monotonic_ns(),"clockId":self.clock_id,
                // Only the console's allow-listed keys may occur here. Do not
                // send transcripts, protocol names, barge-in, or silence_ms.
                "extra":{"coverage":"application"}})
        };
        if self.sender.try_send(body).is_err() {
            self.counters.dropped.fetch_add(1, Ordering::Relaxed);
        }
    }
}

/// One observer per voice session. Its lifetime owns a reference to the shared
/// poster, so the final session dropping also closes and drains its queue.
pub struct SessionTelemetry {
    poster: Arc<Poster>,
    session: String,
    call: String,
    realtime: bool,
    started: Instant,
    turn: u64,
    saw_token: bool,
    saw_audio: bool,
}

impl SessionTelemetry {
    pub fn new(
        config: &Config,
        session: String,
        call: String,
        realtime: bool,
        shutdown: CancellationToken,
    ) -> Option<Self> {
        Some(Self {
            poster: poster(config, shutdown)?,
            session,
            call,
            realtime,
            started: Instant::now(),
            turn: 0,
            saw_token: false,
            saw_audio: false,
        })
    }

    fn emit(
        &self,
        component: &str,
        event: &str,
        point: &str,
        chain: &str,
        turn: u64,
        extra: Value,
    ) {
        self.poster.post(
            point,
            event,
            component,
            chain,
            turn,
            &self.call,
            &self.session,
            self.started.elapsed().as_millis(),
            extra,
        );
    }

    pub fn released(&self) {
        self.emit(
            "s2s",
            "release",
            "opening.released",
            "opening",
            0,
            json!({}),
        );
    }

    pub fn stats(&self) -> Value {
        let stats = &self.poster.counters;
        json!({"format":if self.poster.legacy{"legacy"}else{"v1"},"clock":self.poster.clock_id,
            "sent":stats.sent.load(Ordering::Relaxed),"failed":stats.failed.load(Ordering::Relaxed),"dropped":stats.dropped.load(Ordering::Relaxed)})
    }

    pub fn observe(&mut self, event: &Value, config: &Config) {
        match event["type"].as_str().unwrap_or("") {
            "session.started" => self.emit(
                "s2s",
                "ws_open",
                "agent.connected",
                "lifecycle",
                0,
                json!({"protocol":if self.realtime{"openai-realtime"}else{"gpt-live-1"}}),
            ),
            "golive.speech.started" => {
                self.turn += 1;
                self.emit(
                    "vad",
                    "speech_start",
                    "vad.speech_started",
                    "turn",
                    self.turn,
                    json!({"barge_in":event["barge_in"].as_bool().unwrap_or(false)}),
                );
            }
            "golive.speech.stopped" => {
                self.saw_token = false;
                self.saw_audio = false;
                let silence = event["silence_ms"]
                    .as_u64()
                    .filter(|n| *n > 0)
                    .unwrap_or(config.usize("vad.min_silence_ms", 380) as u64);
                self.emit(
                    "vad",
                    "speech_stop",
                    "vad.speech_stopped",
                    "turn",
                    self.turn,
                    json!({"silence_ms":silence}),
                );
            }
            "session.input_transcript.delta" if event["final"] == true => {
                self.emit("asr", "final", "asr.final", "turn", self.turn, json!({}))
            }
            "response.event" if event["event"]["type"] == "response.output_text.delta" => {
                self.first_token()
            }
            "response.output_text.delta" => self.first_token(),
            "session.output_audio.delta" => {
                if event["item_id"].as_str().unwrap_or("").starts_with("bc_") {
                    return;
                }
                if self.saw_audio {
                    return;
                }
                self.saw_audio = true;
                let chain = if self.turn == 0 { "opening" } else { "turn" };
                let point = if self.turn == 0 {
                    "opening.first_audio_generated"
                } else {
                    "tts.first_audio"
                };
                self.emit("tts", "first_audio", point, chain, self.turn, json!({}));
                self.emit(
                    "phone",
                    "first_audio",
                    "phone.audio_sent",
                    chain,
                    self.turn,
                    json!({"coverage":"application"}),
                );
            }
            "session.closed" => self.emit(
                "s2s",
                "ws_close",
                "agent.disconnected",
                "lifecycle",
                0,
                json!({"reason":event["reason"]}),
            ),
            _ => {}
        }
    }

    fn first_token(&mut self) {
        let first = !self.saw_token;
        self.saw_token = true;
        if first && self.turn > 0 {
            self.emit(
                "llm",
                "first_token",
                "llm.first_token",
                "turn",
                self.turn,
                json!({}),
            );
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use axum::{Json, Router, extract::State, routing::post};

    async fn collector(
        State(events): State<Arc<Mutex<Vec<Value>>>>,
        Json(batch): Json<Vec<Value>>,
    ) -> Json<Value> {
        events.lock().unwrap().extend(batch);
        Json(json!({"ok":true}))
    }

    async fn sink() -> (String, Arc<Mutex<Vec<Value>>>, tokio::task::JoinHandle<()>) {
        let events = Arc::new(Mutex::new(vec![]));
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let url = format!("http://{}/ingest", listener.local_addr().unwrap());
        let router = Router::new()
            .route("/ingest", post(collector))
            .with_state(events.clone());
        let task = tokio::spawn(async move {
            axum::serve(listener, router).await.unwrap();
        });
        (url, events, task)
    }

    async fn wait_count(events: &Arc<Mutex<Vec<Value>>>, count: usize) {
        tokio::time::timeout(Duration::from_secs(3), async {
            while events.lock().unwrap().len() < count {
                tokio::time::sleep(Duration::from_millis(5)).await;
            }
        })
        .await
        .expect("console did not receive expected telemetry");
    }

    #[tokio::test]
    async fn canonical_chain_has_console_safe_fields_and_unique_short_ids() {
        let (url, events, task) = sink().await;
        let mut cfg = Config::default();
        cfg.set("telemetry_url", json!(url));
        let mut session = SessionTelemetry::new(
            &cfg,
            "session".into(),
            "call-1".into(),
            true,
            CancellationToken::new(),
        )
        .unwrap();
        session.observe(&json!({"type":"session.started"}), &cfg);
        session.released();
        session.observe(
            &json!({"type":"golive.speech.started","barge_in":true}),
            &cfg,
        );
        session.observe(
            &json!({"type":"golive.speech.stopped","silence_ms":280}),
            &cfg,
        );
        session.observe(
            &json!({"type":"session.input_transcript.delta","delta":"private words","final":true}),
            &cfg,
        );
        for _ in 0..2 {
            session.observe(&json!({"type":"response.event","event":{"type":"response.output_text.delta","delta":"private answer"}}),&cfg);
        }
        session.observe(
            &json!({"type":"session.output_audio.delta","item_id":"bc_ack","delta":"ignored"}),
            &cfg,
        );
        for _ in 0..2 {
            session.observe(
                &json!({"type":"session.output_audio.delta","item_id":"answer","delta":"ignored"}),
                &cfg,
            );
        }
        session.observe(
            &json!({"type":"session.closed","reason":"close_requested"}),
            &cfg,
        );
        wait_count(&events, 9).await;
        let records = events.lock().unwrap().clone();
        let points: Vec<_> = records
            .iter()
            .map(|v| v["point"].as_str().unwrap())
            .collect();
        assert_eq!(
            points,
            vec![
                "agent.connected",
                "opening.released",
                "vad.speech_started",
                "vad.speech_stopped",
                "asr.final",
                "llm.first_token",
                "tts.first_audio",
                "phone.audio_sent",
                "agent.disconnected"
            ]
        );
        for (index, event) in records.iter().enumerate() {
            assert_eq!(event["schemaVersion"], 1);
            assert_eq!(event["callId"], "call-1");
            assert_eq!(event["backend"], "local");
            assert_eq!(event["extra"], json!({"coverage":"application"}));
            assert!(!event.to_string().contains("private"));
            let id = event["eventId"].as_str().unwrap();
            let parts: Vec<_> = id.split(':').collect();
            assert!(parts[parts.len() - 2].starts_with(&format!(
                "golive-{}-r",
                event["component"].as_str().unwrap()
            )));
            assert_eq!(parts.last().unwrap().parse::<usize>().unwrap(), index + 1);
            assert_eq!(event["sequence"], index + 1);
            if event["chain"] == "turn" {
                assert_eq!(event["turnId"], 1);
            } else {
                assert!(event["turnId"].is_null());
            }
            #[cfg(target_os = "linux")]
            {
                assert!(event["clockId"].as_str().unwrap().starts_with("boot:"));
                assert!(
                    event["monotonicNs"]
                        .as_str()
                        .unwrap()
                        .parse::<u128>()
                        .unwrap()
                        > 0
                );
            }
        }
        tokio::time::timeout(Duration::from_secs(1), async {
            while session.stats()["sent"] != 9 {
                tokio::task::yield_now().await;
            }
        })
        .await
        .unwrap();
        task.abort();
    }

    #[tokio::test]
    async fn legacy_opening_preserves_labels_and_nonblocking_shutdown_drains() {
        let (url, events, task) = sink().await;
        let mut cfg = Config::default();
        cfg.set("telemetry_url", json!(url));
        cfg.set("telemetry_format", json!("legacy"));
        let shutdown = CancellationToken::new();
        let mut session = SessionTelemetry::new(
            &cfg,
            "session-legacy".into(),
            String::new(),
            true,
            shutdown.clone(),
        )
        .unwrap();
        session.observe(&json!({"type":"session.started"}), &cfg);
        session.observe(
            &json!({"type":"session.output_audio.delta","item_id":"opening"}),
            &cfg,
        );
        shutdown.cancel();
        session.observe(
            &json!({"type":"session.closed","reason":"server_shutdown"}),
            &cfg,
        );
        drop(session);
        wait_count(&events, 4).await;
        let records = events.lock().unwrap().clone();
        assert_eq!(records[0]["extra"]["protocol"], "openai-realtime");
        assert_eq!(records[1]["component"], "tts");
        assert_eq!(records[2]["component"], "s2s");
        for event in records {
            assert_eq!(event["extra"]["brain"], "golive");
            assert_eq!(event["extra"]["session"], "session-legacy");
            assert!(event.get("schemaVersion").is_none());
        }
        task.abort();
    }

    #[test]
    fn full_queue_drops_without_blocking_and_unattributed_v1_is_silent() {
        let (sender, mut receiver) = mpsc::channel(1);
        let poster = Poster {
            sender,
            counters: Arc::default(),
            sequence: AtomicU64::new(0),
            clock_id: "boot:test".into(),
            run_id: "rtest".into(),
            legacy: false,
            shutdown: CancellationToken::new(),
        };
        poster.post(
            "agent.connected",
            "ws_open",
            "s2s",
            "lifecycle",
            0,
            "",
            "session",
            0,
            json!({}),
        );
        assert!(receiver.try_recv().is_err());
        for _ in 0..1000 {
            poster.post(
                "phone.audio_sent",
                "first_audio",
                "phone",
                "turn",
                1,
                "call",
                "session",
                0,
                json!({}),
            );
        }
        assert_eq!(poster.counters.dropped.load(Ordering::Relaxed), 999);
        assert!(receiver.try_recv().is_ok());
    }

    #[tokio::test]
    async fn realtime_microphone_turn_reaches_http_console_through_session_events() {
        use base64::{Engine as _, engine::general_purpose::STANDARD};
        use futures_util::{SinkExt, StreamExt};
        use tokio_tungstenite::tungstenite::Message;
        let (url, events, console) = sink().await;
        let mut cfg = Config::default();
        cfg.set("telemetry_url", json!(url));
        cfg.set("vad.min_speech_ms", json!(40));
        cfg.set("vad.min_silence_ms", json!(100));
        cfg.set("duplex.early_final_ms", json!(0));
        cfg.set("duplex.semantic_endpointing", json!(false));
        cfg.env.insert("GOLIVE_MOCK_LLM_RUNE_MS".into(), "1".into());
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let ws_url = format!(
            "ws://{}/v1/realtime?call=telemetry-wire-test&rate=16000",
            listener.local_addr().unwrap()
        );
        let service = tokio::spawn(async move {
            axum::serve(listener, crate::server::router(cfg))
                .await
                .unwrap();
        });
        let (mut socket, _) = tokio_tungstenite::connect_async(ws_url).await.unwrap();
        for index in 0..50 {
            let frame: Vec<_> = (0..320)
                .flat_map(|i| {
                    let sample = if index < 30 {
                        ((i as f64 * std::f64::consts::TAU * 220.0 / 16000.0).sin() * 6000.0) as i16
                    } else {
                        0
                    };
                    sample.to_le_bytes()
                })
                .collect();
            socket
                .send(Message::Text(
                    json!({"type":"input_audio_buffer.append","audio":STANDARD.encode(frame)})
                        .to_string()
                        .into(),
                ))
                .await
                .unwrap();
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
        tokio::time::timeout(Duration::from_secs(5), async {
            while let Some(Ok(message)) = socket.next().await {
                if let Message::Text(text) = message {
                    let event: Value = serde_json::from_str(&text).unwrap();
                    if event["type"] == "response.audio.delta" {
                        break;
                    }
                    assert_ne!(event["type"], "error", "unexpected voice error: {event}");
                }
            }
        })
        .await
        .unwrap();
        socket.close(None).await.unwrap();
        wait_count(&events, 8).await;
        let records = events.lock().unwrap().clone();
        for point in [
            "agent.connected",
            "vad.speech_started",
            "vad.speech_stopped",
            "asr.final",
            "llm.first_token",
            "tts.first_audio",
            "phone.audio_sent",
            "agent.disconnected",
        ] {
            assert_eq!(
                records.iter().filter(|e| e["point"] == point).count(),
                1,
                "missing or duplicate boundary {point}"
            );
        }
        assert!(records.iter().all(|e| e["callId"] == "telemetry-wire-test"));
        console.abort();
        service.abort();
    }
}
