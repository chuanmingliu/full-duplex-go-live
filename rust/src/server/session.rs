//! Per-socket lifecycle and command validation.
use super::*;

struct Session {
    config: Config,
    id: String,
    format: AudioFormat,
    settings: Value,
    engine: Option<EngineHandle>,
    translator: Option<Realtime>,
    held: bool,
    opened: Arc<AtomicBool>,
    started: Arc<AtomicBool>,
    sequence: u64,
    close_reason: Option<String>,
    since: Instant,
    last_input: Instant,
    rate_since: Instant,
    events_in_window: usize,
    audio_in_window: usize,
    delegations: std::collections::VecDeque<String>,
    instruction_chars: usize,
    telemetry: Option<crate::telemetry::SessionTelemetry>,
}

impl Session {
    fn settings(config: &Config, id: &str, format: &AudioFormat) -> Value {
        json!({"id":id,"model":config.string("model","gpt-live-1"),"instructions":config.string("instructions",""),
            "audio":{"format":format.json(),"output":{"voice":config.string("voice",""),"speed":config.f64("speed",1.0)}},
            "delegation":{"type":config.string("duplex.delegation_mode","responses")},"store":false})
    }

    async fn command(&self, command: EngineCommand) -> Result<()> {
        let Some(engine) = &self.engine else {
            bail!("session has not started");
        };
        tokio::time::timeout(Duration::from_secs(1), engine.commands.send(command))
            .await
            .context("engine command queue is full")?
            .context("engine stopped")
    }

    async fn start(&mut self, requested: &Value) -> Result<Vec<Value>> {
        if self.engine.is_some() {
            bail!("session has already started");
        }
        if requested["store"].as_bool().unwrap_or(false) {
            bail!("session storage is not supported in the Rust service");
        }
        if let Some(input) = requested.get("input") {
            let input = input.as_array().context("session.input must be an array")?;
            if input.len() > 128 {
                bail!("session.input accepts at most 128 messages");
            }
            let mut history = Vec::with_capacity(input.len());
            for item in input {
                if item["type"].as_str().is_some_and(|t| t != "message") {
                    bail!("session.input supports message items only");
                }
                let role = match item["role"].as_str() {
                    Some("developer" | "system") => "system",
                    Some("user") => "user",
                    Some("assistant") => "assistant",
                    _ => bail!("session.input role must be developer, system, user or assistant"),
                };
                if item.get("tool_calls").is_some() || item.get("tool_call_id").is_some() {
                    bail!("session.input does not support tool exchanges");
                }
                let text = protocol::content_text(&item["content"])
                    .context("session.input content must contain text")?;
                history.push(json!({"role":role,"content":text}));
            }
            self.config.set("session_input", json!(history));
        }
        let realtime = self.translator.is_some();
        self.format = if realtime {
            AudioFormat::from_realtime(requested, self.format.rate)?
        } else {
            AudioFormat::from_live(requested, self.format.rate)?
        };
        if !realtime {
            let default_mode = self.config.string("duplex.delegation_mode", "responses");
            let mode = requested["delegation"]["type"]
                .as_str()
                .unwrap_or(&default_mode);
            if !["responses", "client"].contains(&mode) {
                bail!("delegation.type must be responses or client");
            }
            if mode == "client" && requested["delegation"]["responses"].is_object() {
                bail!("client delegation has no responses settings");
            }
            if requested["golive"]["client_delegation"].as_str() == Some("auto") {
                bail!("automatic client delegation selection is not supported");
            }
            self.config.set("duplex.delegation_mode", json!(mode));
        }
        if !realtime {
            if let Some(model) = requested["model"].as_str().filter(|s| !s.is_empty()) {
                self.config.set("model", json!(model));
            }
        }
        if let Some(instructions) = requested["instructions"].as_str().filter(|s| !s.is_empty()) {
            self.config.set("instructions", json!(instructions));
        }
        let voice = if realtime {
            requested
                .pointer("/audio/output/voice")
                .and_then(Value::as_str)
                .or_else(|| requested["voice"].as_str())
        } else {
            requested
                .pointer("/audio/output/voice")
                .and_then(Value::as_str)
        };
        if let Some(voice) = voice.filter(|s| !s.is_empty()) {
            let alias = self
                .config
                .value("voice_aliases")
                .and_then(|v| v.get(voice))
                .and_then(Value::as_str)
                .map(str::to_owned);
            self.config
                .set("voice", json!(alias.unwrap_or_else(|| voice.into())));
        }
        if let Some(speed) = requested
            .pointer("/audio/output/speed")
            .and_then(Value::as_f64)
        {
            if !(0.5..=2.0).contains(&speed) {
                bail!("audio.output.speed must be between 0.5 and 2.0");
            }
            self.config.set("speed", json!(speed));
        }
        if let Some(golive) = requested["golive"].as_object() {
            for name in ["asr", "llm", "tts"] {
                if let Some(value) = golive.get(name).and_then(Value::as_str) {
                    if !self.config.bool("allow_provider_override", false)
                        && value != self.config.string(name, "mock")
                    {
                        bail!("provider override is disabled");
                    }
                    self.config.set(name, json!(value));
                }
            }
            for name in [
                "backchannel",
                "speculative",
                "on_new_query",
                "user_backchannel_phrases",
            ] {
                if let Some(value) = golive.get(name) {
                    self.config.set(&format!("duplex.{name}"), value.clone());
                }
            }
            if let Some(silence) = golive.get("min_silence_ms").and_then(Value::as_u64) {
                if !(100..=3000).contains(&silence) {
                    bail!("min_silence_ms must be between 100 and 3000");
                }
                self.config.set("vad.min_silence_ms", json!(silence));
            }
        }
        if realtime && self.config.bool("realtime.honor_turn_detection", false) {
            let td = requested
                .pointer("/audio/input/turn_detection")
                .unwrap_or(&requested["turn_detection"]);
            if let Some(ms) = td["silence_duration_ms"]
                .as_u64()
                .or_else(|| td["silenceDurationMs"].as_u64())
                .filter(|n| *n > 0)
            {
                let floor = self
                    .config
                    .usize("realtime.min_silence_floor_ms", 100)
                    .min(3000) as u64;
                self.config
                    .set("vad.min_silence_ms", json!(ms.clamp(floor, 3000)));
            }
        }
        let backend = if realtime {
            requested
        } else {
            &requested["delegation"]["responses"]
        };
        self.apply_backend(backend)?;
        self.settings = Self::settings(&self.config, &self.id, &self.format);
        if realtime {
            self.settings["turn_detection"] = json!({"type":"server_vad","silence_duration_ms":self.config.usize("vad.min_silence_ms",380)});
        }
        if !realtime && requested["delegation"]["responses"].is_object() {
            self.settings["delegation"]["responses"] = requested["delegation"]["responses"].clone();
        }
        crate::provider::preflight(&self.config)?;
        self.engine = Some(engine::spawn(self.config.clone()));
        self.started.store(true, Ordering::Relaxed);
        if self.held {
            self.command(EngineCommand::Mute(true)).await?;
        }
        let greeting = if realtime {
            String::new()
        } else {
            requested["golive"]["greeting"]
                .as_str()
                .map(str::to_owned)
                .unwrap_or_else(|| self.config.string("greeting", ""))
        };
        // Commands can start work immediately, but the socket writes these
        // acknowledgements before it polls the engine's event stream.
        if !greeting.is_empty() {
            self.command(EngineCommand::Speak(greeting)).await?;
        }
        Ok(vec![
            json!({"type":"session.started","session":self.settings}),
        ])
    }

    fn apply_backend(&mut self, backend: &Value) -> Result<()> {
        if self.translator.is_none() {
            if let Some(model) = backend["model"].as_str() {
                self.config.set("backend_model", json!(model));
            }
            if let Some(instructions) = backend["instructions"].as_str() {
                self.config.set("backend_instructions", json!(instructions));
            }
        }
        if let Some(tools) = backend.get("tools") {
            self.config.set("tools", client_tool_schemas(tools)?);
        }
        for name in ["tool_choice", "parallel_tool_calls"] {
            if let Some(value) = backend.get(name) {
                self.config.set(name, value.clone());
            }
        }
        if let Some(value) = backend.get("max_output_tokens") {
            let tokens = value
                .as_u64()
                .context("max_output_tokens must be an integer")?;
            if !(16..=32768).contains(&tokens) {
                bail!("max_output_tokens must be between 16 and 32768");
            }
            self.config.set("max_output_tokens", json!(tokens));
        }
        Ok(())
    }

    fn roll_rate(&mut self) {
        if self.rate_since.elapsed() >= Duration::from_secs(1) {
            self.rate_since = Instant::now();
            self.audio_in_window = 0;
            self.events_in_window = 0;
        }
    }

    async fn audio(&mut self, wire: Vec<u8>) -> Result<()> {
        self.roll_rate();
        self.audio_in_window += wire.len();
        let limit = self.config.usize("duplex.max_audio_kbps", 2048);
        if limit > 0 && self.audio_in_window > limit * 1000 / 8 {
            self.close_reason = Some("rate_limited".into());
            bail!("audio rate limit exceeded");
        }
        self.last_input = Instant::now();
        let pcm = self.format.decode(&wire)?;
        if !self.held {
            self.command(EngineCommand::Audio(pcm)).await?;
        }
        Ok(())
    }

    async fn process(&mut self, event: Value) -> Vec<Value> {
        let id = event["event_id"].as_str().unwrap_or("").to_owned();
        match self.process_inner(&event).await {
            Ok(mut events) => {
                if !id.is_empty() {
                    for event in &mut events {
                        event["client_event_id"] = id.clone().into();
                    }
                }
                events
            }
            Err(error) => {
                let message = error.to_string();
                let kind = event["type"].as_str().unwrap_or("");
                let code = if message.starts_with("unsupported client event") {
                    "unknown_event_type"
                } else if message.contains("requires a type") {
                    "malformed_event"
                } else if message.contains("already started") {
                    "already_started"
                } else if message.starts_with("send session.") {
                    "session_not_started"
                } else if message.contains("unknown delegation_id") {
                    "unknown_delegation"
                } else if message.contains("immutable after startup") {
                    "immutable_field_update"
                } else if self.close_reason.as_deref() == Some("rate_limited") {
                    "rate_limited"
                } else if kind == "session.input_audio.append"
                    || kind == "input_audio_buffer.append"
                {
                    "bad_audio"
                } else if kind == "session.start" || kind == "session.update" {
                    "invalid_session"
                } else {
                    "invalid_request"
                };
                vec![protocol::error(code, message, &id)]
            }
        }
    }

    async fn process_inner(&mut self, event: &Value) -> Result<Vec<Value>> {
        let kind = event["type"]
            .as_str()
            .filter(|s| !s.is_empty())
            .context("event requires a type")?;
        let realtime = self.translator.is_some();
        let is_audio = kind == "session.input_audio.append" || kind == "input_audio_buffer.append";
        if !is_audio {
            self.roll_rate();
            self.events_in_window += 1;
            let max = self.config.usize("duplex.max_events_per_second", 200);
            if max > 0 && self.events_in_window > max {
                self.close_reason = Some("rate_limited".into());
                bail!("event rate limit exceeded");
            }
        }
        if self.engine.is_none() {
            if kind == "session.start" && !realtime || kind == "session.update" && realtime {
                return self.start(&event["session"]).await;
            }
            if realtime && kind == "input_audio_buffer.append" {
                let mut events = self.start(&json!({})).await?;
                self.audio(
                    STANDARD.decode(event["audio"].as_str().context("audio must be base64")?)?,
                )
                .await?;
                return Ok(std::mem::take(&mut events));
            }
            bail!(
                "send {} before any other event",
                if realtime {
                    "session.update"
                } else {
                    "session.start"
                }
            );
        }
        match kind {
            "session.start" => bail!("session has already started"),
            "session.update" if realtime => Ok(vec![
                json!({"type":"session.updated","session":self.settings}),
            ]),
            "session.update" => {
                let update = &event["session"];
                if update.get("audio").and_then(|v| v.get("format")).is_some()
                    || update["model"]
                        .as_str()
                        .is_some_and(|s| s != self.settings["model"].as_str().unwrap_or(""))
                    || update["instructions"]
                        .as_str()
                        .is_some_and(|s| s != self.settings["instructions"].as_str().unwrap_or(""))
                    || update
                        .pointer("/audio/output/voice")
                        .and_then(Value::as_str)
                        .is_some_and(|s| {
                            s != self.settings["audio"]["output"]["voice"]
                                .as_str()
                                .unwrap_or("")
                        })
                    || update
                        .pointer("/delegation/type")
                        .and_then(Value::as_str)
                        .is_some_and(|s| {
                            s != self.settings["delegation"]["type"]
                                .as_str()
                                .unwrap_or("responses")
                        })
                {
                    bail!(
                        "model, instructions, voice, format and delegation type are immutable after startup"
                    );
                }
                if let Some(backend) = update.pointer("/delegation/responses") {
                    if self.settings["delegation"]["type"] != "responses" {
                        bail!("client delegation has no responses settings");
                    }
                    self.apply_backend(backend)?;
                    let mut merged = self.settings["delegation"]["responses"]
                        .as_object()
                        .cloned()
                        .unwrap_or_default();
                    if let Some(object) = backend.as_object() {
                        merged.extend(object.clone());
                    }
                    self.settings["delegation"]["responses"] = Value::Object(merged);
                    self.command(EngineCommand::Update(self.config.clone()))
                        .await?;
                }
                Ok(vec![
                    json!({"type":"session.updated","session":self.settings}),
                ])
            }
            "session.input_audio.append" if !realtime => {
                self.audio(
                    STANDARD.decode(event["audio"].as_str().context("audio must be base64")?)?,
                )
                .await?;
                Ok(vec![])
            }
            "input_audio_buffer.append" if realtime => {
                self.audio(
                    STANDARD.decode(event["audio"].as_str().context("audio must be base64")?)?,
                )
                .await?;
                Ok(vec![])
            }
            "session.input_audio.mute" | "session.input_audio.unmute" if !realtime => {
                let muted = kind.ends_with(".mute");
                self.command(EngineCommand::Mute(muted)).await?;
                Ok(vec![
                    json!({"type":if muted {"session.input_audio.muted"} else {"session.input_audio.unmuted"}}),
                ])
            }
            "session.instructions.append"
            | "session.thinking.append"
            | "session.commentary.append"
                if !realtime =>
            {
                let delegation = event["delegation_id"].as_str().unwrap_or("");
                if !delegation.is_empty()
                    && !self.delegations.iter().any(|known| known == delegation)
                {
                    bail!("unknown delegation_id");
                }
                let content = event["content"].as_str().context("content must be text")?;
                if content.chars().count() > self.config.usize("duplex.max_append_chars", 4000) {
                    bail!("append is too long");
                }
                if kind == "session.instructions.append" {
                    let count = content.chars().count();
                    if self.instruction_chars + count
                        > 32 * self.config.usize("duplex.max_append_chars", 4000)
                    {
                        bail!("total appended instructions exceed session limit");
                    }
                    self.instruction_chars += count;
                }
                match kind {
                    "session.instructions.append" => {
                        self.command(EngineCommand::AppendInstructions(content.into()))
                            .await?;
                        let old = self.config.string("instructions", "");
                        self.config
                            .set("instructions", json!(format!("{old}\n{content}")));
                    }
                    "session.thinking.append" => {
                        self.command(EngineCommand::AppendThinking(content.into()))
                            .await?
                    }
                    _ => {
                        self.command(EngineCommand::Commentary(content.into()))
                            .await?
                    }
                }
                Ok(vec![
                    json!({"type":kind.replace(".append",".appended"),"start_ms":self.since.elapsed().as_millis(),"end_ms":self.since.elapsed().as_millis(),"delegation_id":delegation}),
                ])
            }
            "response.item.create" if !realtime => {
                self.item(&event["item"]).await?;
                Ok(vec![])
            }
            "conversation.item.create" if realtime => {
                self.item(&event["item"]).await?;
                Ok(vec![])
            }
            "response.create" => {
                self.command(EngineCommand::Generate).await?;
                Ok(vec![])
            }
            "response.cancel" if realtime => {
                if !self.translator.as_ref().unwrap().cancel_is_echo() {
                    self.command(EngineCommand::Cancel).await?;
                }
                Ok(vec![])
            }
            "input_audio_buffer.commit"
            | "input_audio_buffer.clear"
            | "output_audio_buffer.clear"
            | "conversation.item.truncate"
                if realtime =>
            {
                Ok(vec![])
            }
            "session.close" if !realtime => {
                self.close_reason = Some("close_requested".into());
                Ok(vec![])
            }
            _ => bail!("unsupported client event {kind}"),
        }
    }

    async fn item(&mut self, item: &Value) -> Result<()> {
        match item["type"].as_str().unwrap_or("message") {
            "message" if item["role"].as_str().unwrap_or("user") == "user" => {
                let text = protocol::content_text(&item["content"])
                    .context("message.content must contain text")?;
                if text.is_empty() {
                    bail!("message content is empty");
                }
                if text.chars().count() > self.config.usize("duplex.max_append_chars", 4000) {
                    bail!("message is too long");
                }
                self.command(EngineCommand::UserText(text)).await
            }
            "function_call_output" => {
                let id = item["call_id"]
                    .as_str()
                    .filter(|s| !s.is_empty())
                    .context("function result requires call_id")?;
                let output = item["output"]
                    .as_str()
                    .map(str::to_owned)
                    .unwrap_or_else(|| item["output"].to_string());
                self.command(EngineCommand::ToolResult {
                    id: id.into(),
                    output,
                })
                .await
            }
            _ => bail!("unsupported conversation item; use user message or function_call_output"),
        }
    }

    async fn emit(&mut self, socket: &mut WebSocket, mut event: Value) -> Result<()> {
        if let Some(telemetry) = &mut self.telemetry {
            telemetry.observe(&event, &self.config);
        }
        if event["type"] == "session.delegation.created" {
            if let Some(id) = event["delegation"]["id"].as_str() {
                if self.delegations.len() >= 128 {
                    self.delegations.pop_front();
                }
                self.delegations.push_back(id.into());
            }
        }
        self.format.encode_event(&mut event)?;
        if event["type"] == "session.output_audio.delta"
            && self.config.bool("duplex.legacy_audio_field", false)
        {
            event["audio"] = event["delta"].clone();
        }
        let events = if let Some(rt) = &mut self.translator {
            rt.translate(event)
        } else {
            vec![event]
        };
        for mut event in events {
            self.sequence += 1;
            event["event_id"] = json!(format!("event_{}", self.sequence));
            tokio::time::timeout(
                Duration::from_secs(5),
                socket.send(Message::Text(event.to_string().into())),
            )
            .await
            .context("client is not reading")??;
        }
        Ok(())
    }
}

/// Clients can describe functions, but only the trusted startup configuration
/// supplies HTTP executors and the credentials those executors receive.
fn client_tool_schemas(value: &Value) -> Result<Value> {
    let tools = value.as_array().context("tools must be an array")?;
    if tools.len() > 128 {
        bail!("tools accepts at most 128 functions");
    }
    let mut schemas = Vec::with_capacity(tools.len());
    for tool in tools {
        if tool["type"] != "function" {
            bail!("only function tools are supported");
        }
        let function = tool.get("function").unwrap_or(tool);
        for field in [
            "url",
            "async",
            "timeout_ms",
            "eta_s",
            "headers",
            "token",
            "token_file",
        ] {
            if tool.get(field).is_some() || function.get(field).is_some() {
                bail!("client tools cannot configure server executors");
            }
        }
        let name = function["name"]
            .as_str()
            .filter(|s| !s.is_empty())
            .context("function requires a name")?;
        if name.len() > 64
            || !name
                .bytes()
                .all(|b| b.is_ascii_alphanumeric() || b == b'_' || b == b'-')
        {
            bail!("invalid function name");
        }
        let mut safe = json!({"name":name});
        for field in ["description", "parameters", "strict"] {
            if let Some(value) = function.get(field) {
                safe[field] = value.clone();
            }
        }
        schemas.push(json!({"type":"function","function":safe}));
    }
    Ok(json!(schemas))
}

async fn next_engine(engine: &mut Option<EngineHandle>) -> Option<Value> {
    if let Some(engine) = engine {
        engine.events.recv().await
    } else {
        std::future::pending().await
    }
}

#[allow(clippy::too_many_arguments)]
pub(super) async fn run_socket(
    mut socket: WebSocket,
    app: App,
    query: HashMap<String, String>,
    id: String,
    rate: u32,
    realtime: bool,
    mut control: mpsc::Receiver<Control>,
    opened: Arc<AtomicBool>,
    started: Arc<AtomicBool>,
    _registration: Registration,
) {
    let format = AudioFormat::new("audio/pcm", rate).expect("validated rate");
    let dialect = query
        .get("dialect")
        .cloned()
        .unwrap_or_else(|| app.config.string("realtime.dialect", "beta"));
    let held = realtime
        && query
            .get("hold")
            .map(|v| v == "1" || v == "true")
            .unwrap_or_else(|| app.config.bool("realtime.hold_for_opening", false));
    let call = ["call", "call_id", "callId"]
        .iter()
        .find_map(|key| query.get(*key))
        .cloned()
        .unwrap_or_default();
    let telemetry = crate::telemetry::SessionTelemetry::new(
        &app.config,
        id.clone(),
        call,
        realtime,
        app.shutdown.clone(),
    );
    let mut config = app.config.clone();
    if config.value("_server_tools").is_none() {
        config.set(
            "_server_tools",
            app.config.value("tools").cloned().unwrap_or(json!([])),
        );
    }
    let mut session = Session {
        settings: Session::settings(&app.config, &id, &format),
        config,
        id,
        format,
        engine: None,
        translator: realtime.then(|| Realtime::new(dialect == "ga")),
        held,
        opened,
        started,
        sequence: 0,
        close_reason: None,
        since: Instant::now(),
        last_input: Instant::now(),
        rate_since: Instant::now(),
        events_in_window: 0,
        audio_in_window: 0,
        delegations: std::collections::VecDeque::new(),
        instruction_chars: 0,
        telemetry,
    };
    if realtime {
        let created = json!({"type":"session.created","event_id":"event_0","session":protocol::realtime_session(&session.settings)});
        if !matches!(
            tokio::time::timeout(
                Duration::from_secs(5),
                socket.send(Message::Text(created.to_string().into()))
            )
            .await,
            Ok(Ok(()))
        ) {
            return;
        }
    }
    let mut timer = tokio::time::interval(Duration::from_millis(100));
    loop {
        let outgoing = tokio::select! {
            _ = app.shutdown.cancelled() => {session.close_reason=Some("server_shutdown".into()); vec![]},
            _ = timer.tick() => {
                let max = session.config.usize("duplex.session_max_seconds",3600);
                let idle = session.config.usize("duplex.idle_timeout_seconds",120);
                if max > 0 && session.since.elapsed().as_secs() >= max as u64 {session.close_reason=Some("expired".into());}
                if idle > 0 && session.last_input.elapsed().as_secs() >= idle as u64 {session.close_reason=Some("expired".into());}
                if session.held && session.engine.is_some() && session.since.elapsed().as_secs() >= session.config.usize("realtime.hold_timeout_seconds",45) as u64 {
                    session.held=false;
                    let _ = session.command(EngineCommand::Mute(false)).await;
                }
                vec![]
            },
            Some(Control::Open{greeting,reply}) = control.recv() => {
                let accept = session.engine.is_some() && !session.opened.swap(true,Ordering::Relaxed);
                if accept {
                    if let Some(telemetry)=&session.telemetry {telemetry.released();}
                    session.held=false;
                    let unmuted = session.command(EngineCommand::Mute(false)).await.is_ok();
                    let spoken = greeting.is_empty() || session.command(EngineCommand::Speak(greeting)).await.is_ok();
                    let _ = reply.send(unmuted && spoken);
                } else {let _ = reply.send(false);}
                vec![]
            },
            event = next_engine(&mut session.engine) => {
                match event {
                    Some(event) if event["type"]=="golive.hangup" => {session.close_reason=Some("assistant_hangup".into()); vec![]},
                    Some(event) => vec![event],
                    None => {session.close_reason=Some("error".into()); vec![]},
                }
            },
            incoming = socket.recv() => {
                match incoming {
                    Some(Ok(Message::Text(text))) => match serde_json::from_str(&text) {
                        Ok(event) => session.process(event).await,
                        Err(_) => vec![protocol::error("malformed_event","event must be a JSON object","")],
                    },
                    Some(Ok(Message::Binary(bytes))) => match session.audio(bytes.to_vec()).await {
                        Ok(())=>vec![],Err(error)=>vec![protocol::error("bad_audio",error,"")],
                    },
                    Some(Ok(Message::Ping(payload))) => {
                        if !matches!(tokio::time::timeout(Duration::from_secs(5),socket.send(Message::Pong(payload))).await,Ok(Ok(()))) {break;}
                        vec![]
                    },
                    Some(Ok(Message::Pong(_))) => vec![],
                    _ => break,
                }
            },
        };
        let mut failed = false;
        for event in outgoing {
            if session.emit(&mut socket, event).await.is_err() {
                failed = true;
                break;
            }
        }
        if failed || session.close_reason.is_some() {
            break;
        }
    }
    if let Some(engine) = &session.engine {
        engine.cancel.cancel();
    }
    let reason = session
        .close_reason
        .clone()
        .unwrap_or_else(|| "connection_lost".into());
    let closed = json!({"type":"session.closed","reason":reason,"session":session.settings});
    let _ = session.emit(&mut socket, closed).await;
    let _ = tokio::time::timeout(Duration::from_secs(1), socket.send(Message::Close(None))).await;
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn client_function_schemas_cannot_define_authenticated_executors() {
        for field in ["url", "async", "timeout_ms", "headers", "token_file"] {
            let mut tool =
                json!({"type":"function","name":"lookup","parameters":{"type":"object"}});
            tool[field] = json!("attacker-controlled");
            assert!(client_tool_schemas(&json!([tool.clone()])).is_err());
            let nested = json!({"type":"function","function":tool});
            assert!(client_tool_schemas(&json!([nested])).is_err());
        }
        assert_eq!(client_tool_schemas(&json!([{"type":"function","name":"lookup","parameters":{"type":"object"},"metadata":{"url":"ignored"}}])).unwrap(),
            json!([{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]));
    }
}
