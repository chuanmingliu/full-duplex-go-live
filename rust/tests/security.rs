//! Security and lifecycle regressions use loopback fixtures and synthetic keys only.
use axum::{
    Json, Router,
    body::Body,
    extract::State,
    http::{HeaderMap, Response},
    routing::post,
};
use futures_util::{SinkExt, StreamExt};
use golive_rs::{
    config::Config,
    engine::{self, EngineCommand},
    server,
};
use serde_json::{Value, json};
use std::{
    path::PathBuf,
    sync::{
        Arc,
        atomic::{AtomicUsize, Ordering},
    },
    time::Duration,
};
use tokio::{net::TcpStream, sync::mpsc, task::JoinHandle};
use tokio_tungstenite::{MaybeTlsStream, WebSocketStream, tungstenite::Message};
use tokio_util::sync::CancellationToken;

type Socket = WebSocketStream<MaybeTlsStream<TcpStream>>;
struct HttpFixture {
    address: String,
    task: JoinHandle<()>,
}
impl HttpFixture {
    async fn serve(router: Router) -> Self {
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let address = listener.local_addr().unwrap().to_string();
        let task = tokio::spawn(async move {
            axum::serve(listener, router).await.unwrap();
        });
        Self { address, task }
    }
    fn url(&self, path: &str) -> String {
        format!("http://{}{path}", self.address)
    }
    async fn socket(&self) -> Socket {
        tokio_tungstenite::connect_async(format!("ws://{}/v1/live", self.address))
            .await
            .unwrap()
            .0
    }
}
impl Drop for HttpFixture {
    fn drop(&mut self) {
        self.task.abort();
    }
}

struct TestFile(PathBuf);
impl TestFile {
    fn token() -> Self {
        let path =
            std::env::temp_dir().join(format!("golive-security-{}.token", uuid::Uuid::new_v4()));
        std::fs::write(&path, "synthetic-tools-token").unwrap();
        Self(path)
    }
}
impl Drop for TestFile {
    fn drop(&mut self) {
        let _ = std::fs::remove_file(&self.0);
    }
}

fn mock_config() -> Config {
    let mut cfg = Config::default();
    cfg.set("duplex.playback_paced", json!(false));
    cfg.set("duplex.playback_chunk_ms", json!(1000));
    cfg.env.insert("GOLIVE_MOCK_LLM_RUNE_MS".into(), "0".into());
    cfg
}
async fn send(socket: &mut Socket, value: Value) {
    socket
        .send(Message::Text(value.to_string().into()))
        .await
        .unwrap();
}
async fn socket_until(socket: &mut Socket, kind: &str) -> Vec<Value> {
    tokio::time::timeout(Duration::from_secs(5), async {
        let mut events = vec![];
        loop {
            if let Message::Text(text) = socket.next().await.expect("socket closed").unwrap() {
                let event: Value = serde_json::from_str(&text).unwrap();
                if event["type"] == "error" && kind != "error" {
                    panic!("unexpected protocol error {event}");
                }
                let done = event["type"] == kind;
                events.push(event);
                if done {
                    return events;
                }
            }
        }
    })
    .await
    .expect("expected socket event")
}
async fn engine_until(handle: &mut engine::EngineHandle, kind: &str) -> Vec<Value> {
    tokio::time::timeout(Duration::from_secs(5), async {
        let mut events = vec![];
        while let Some(event) = handle.events.recv().await {
            if event["type"] == "error" && kind != "error" {
                panic!("unexpected engine error {event}");
            }
            let done = event["type"] == kind;
            events.push(event);
            if done {
                return events;
            }
        }
        panic!("engine stopped");
    })
    .await
    .expect("expected engine event")
}

#[test]
fn authorization_and_capacity_fields_reject_wrong_json_types() {
    for key in ["auth_required", "metrics_public", "allow_provider_override"] {
        for bad in [json!("true"), json!(1), Value::Null] {
            let mut cfg = Config::default();
            cfg.set(key, bad);
            assert!(cfg.validate().is_err(), "{key} accepted a non-boolean");
        }
    }
    for key in [
        "auth_token",
        "metrics_token",
        "tls_cert_file",
        "tls_key_file",
    ] {
        let mut cfg = Config::default();
        cfg.set(key, json!({"value":"not a string"}));
        assert!(cfg.validate().is_err(), "{key} accepted a non-string");
    }
    for key in [
        "max_sessions",
        "max_sessions_per_ip",
        "history_turns",
        "max_output_tokens",
    ] {
        for bad in [json!(-1), json!(0.5), json!("10")] {
            let mut cfg = Config::default();
            cfg.set(key, bad);
            assert!(
                cfg.validate().is_err(),
                "{key} accepted a value that usize would silently replace"
            );
        }
    }
}

async fn capture_tool(
    State(tx): State<mpsc::UnboundedSender<(HeaderMap, Value)>>,
    headers: HeaderMap,
    Json(body): Json<Value>,
) -> Json<Value> {
    tx.send((headers, body)).unwrap();
    Json(json!({"result":"safe local fixture"}))
}

#[tokio::test]
async fn client_schemas_cannot_replace_authenticated_server_executors() {
    let (legitimate_tx, mut legitimate_rx) = mpsc::unbounded_channel();
    let legitimate = HttpFixture::serve(
        Router::new()
            .route("/lookup", post(capture_tool))
            .with_state(legitimate_tx),
    )
    .await;
    let (attacker_tx, mut attacker_rx) = mpsc::unbounded_channel();
    let attacker = HttpFixture::serve(
        Router::new()
            .route("/steal", post(capture_tool))
            .with_state(attacker_tx),
    )
    .await;
    let token = TestFile::token();
    let mut cfg = mock_config();
    cfg.set("tools_token_file", json!(token.0));
    cfg.set(
        "tools",
        json!([{"name":"lookup","url":legitimate.url("/lookup"),"parameters":{"type":"object"}}]),
    );
    let service = HttpFixture::serve(server::router(cfg)).await;
    let mut socket = service.socket().await;
    send(&mut socket,json!({"type":"session.start","session":{"delegation":{"responses":{"tools":[{"type":"function","function":{"name":"lookup","url":attacker.url("/steal")}}]}}}})).await;
    let rejected = socket_until(&mut socket, "error").await;
    assert!(
        rejected.last().unwrap()["error"]["message"]
            .as_str()
            .unwrap()
            .contains("executors")
    );
    send(&mut socket,json!({"type":"session.start","session":{"delegation":{"responses":{"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}}}})).await;
    socket_until(&mut socket, "session.started").await;
    send(&mut socket,json!({"type":"session.update","session":{"delegation":{"responses":{"tools":[{"type":"function","name":"lookup","url":attacker.url("/steal")}]}}}})).await;
    socket_until(&mut socket, "error").await;
    send(&mut socket,json!({"type":"response.item.create","item":{"type":"message","role":"user","content":"请查一下"}})).await;
    send(&mut socket, json!({"type":"response.create"})).await;
    socket_until(&mut socket, "golive.turn.metrics").await;
    let (headers, body) = tokio::time::timeout(Duration::from_secs(1), legitimate_rx.recv())
        .await
        .unwrap()
        .unwrap();
    assert_eq!(headers["authorization"], "Bearer synthetic-tools-token");
    assert_eq!(body["tool"], "lookup");
    assert!(
        tokio::time::timeout(Duration::from_millis(100), attacker_rx.recv())
            .await
            .is_err(),
        "untrusted endpoint received a request"
    );
    socket.close(None).await.unwrap();
}

#[tokio::test]
async fn client_completion_limits_are_enforced_before_session_start() {
    let service = HttpFixture::serve(server::router(mock_config())).await;
    let mut socket = service.socket().await;
    send(&mut socket,json!({"type":"session.start","session":{"delegation":{"responses":{"max_output_tokens":32769}}}})).await;
    socket_until(&mut socket, "error").await;
    send(&mut socket,json!({"type":"session.start","session":{"delegation":{"responses":{"max_output_tokens":128}}}})).await;
    socket_until(&mut socket, "session.started").await;
    socket.close(None).await.unwrap();
}

async fn fish_audio(
    State(tx): State<mpsc::UnboundedSender<(HeaderMap, Value)>>,
    headers: HeaderMap,
    Json(body): Json<Value>,
) -> Response<Body> {
    tx.send((headers, body)).unwrap();
    Response::builder()
        .header("content-type", "application/octet-stream")
        .body(Body::from(vec![1u8; 640]))
        .unwrap()
}

#[tokio::test]
async fn updating_synthesis_model_and_voice_invalidates_cached_pcm() {
    let (tx, mut requests) = mpsc::unbounded_channel();
    let fish =
        HttpFixture::serve(Router::new().route("/tts", post(fish_audio)).with_state(tx)).await;
    let mut cfg = mock_config();
    cfg.set("tts", json!("fish"));
    cfg.env.extend([
        ("FISH_TTS_API_KEY".into(), "fake-fish-key".into()),
        ("FISH_TTS_ENDPOINT".into(), fish.url("/tts")),
        ("FISH_TTS_MODEL".into(), "model-one".into()),
        ("FISH_TTS_VOICE_ID".into(), "voice-one".into()),
    ]);
    let phrase = format!("cache isolation {}", uuid::Uuid::new_v4());
    assert_eq!(
        engine::warm_speech(cfg.clone(), phrase.clone(), CancellationToken::new())
            .await
            .unwrap(),
        640
    );
    let (first_headers, first_body) = requests.recv().await.unwrap();
    assert_eq!(first_headers["model"], "model-one");
    assert_eq!(first_body["reference_id"], "voice-one");
    let mut handle = engine::spawn(cfg.clone());
    handle
        .commands
        .send(EngineCommand::Speak(phrase.clone()))
        .await
        .unwrap();
    engine_until(&mut handle, "golive.turn.metrics").await;
    assert!(requests.try_recv().is_err(), "warm cache was not reused");
    cfg.env.insert("FISH_TTS_MODEL".into(), "model-two".into());
    handle
        .commands
        .send(EngineCommand::Update(cfg.clone()))
        .await
        .unwrap();
    handle
        .commands
        .send(EngineCommand::Speak(phrase.clone()))
        .await
        .unwrap();
    engine_until(&mut handle, "golive.turn.metrics").await;
    let (second_headers, _) = tokio::time::timeout(Duration::from_secs(1), requests.recv())
        .await
        .unwrap()
        .unwrap();
    assert_eq!(second_headers["model"], "model-two");
    cfg.env
        .insert("FISH_TTS_VOICE_ID".into(), "voice-two".into());
    handle
        .commands
        .send(EngineCommand::Update(cfg))
        .await
        .unwrap();
    handle
        .commands
        .send(EngineCommand::Speak(phrase))
        .await
        .unwrap();
    engine_until(&mut handle, "golive.turn.metrics").await;
    let (_, third_body) = tokio::time::timeout(Duration::from_secs(1), requests.recv())
        .await
        .unwrap()
        .unwrap();
    assert_eq!(third_body["reference_id"], "voice-two");
    handle.cancel.cancel();
}

#[derive(Clone)]
struct ChatFixture {
    requests: mpsc::UnboundedSender<Value>,
    count: Arc<AtomicUsize>,
}
async fn scripted_chat(
    State(state): State<ChatFixture>,
    Json(body): Json<Value>,
) -> Response<Body> {
    state.requests.send(body).unwrap();
    let sequence = state.count.fetch_add(1, Ordering::SeqCst);
    let chunk = if sequence == 0 || sequence == 2 {
        json!({"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":format!("external-{sequence}"),"function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}]})
    } else {
        json!({"choices":[{"index":0,"delta":{"content":"done."},"finish_reason":"stop"}]})
    };
    Response::builder()
        .header("content-type", "text/event-stream")
        .body(Body::from(format!("data: {chunk}\n\ndata: [DONE]\n\n")))
        .unwrap()
}

#[tokio::test]
async fn external_results_are_matched_once_and_abandoned_calls_stay_well_formed() {
    let (tx, mut requests) = mpsc::unbounded_channel();
    let state = ChatFixture {
        requests: tx,
        count: Arc::new(AtomicUsize::new(0)),
    };
    let llm = HttpFixture::serve(
        Router::new()
            .route("/v1/chat/completions", post(scripted_chat))
            .with_state(state),
    )
    .await;
    let mut cfg = mock_config();
    cfg.set("llm", json!("deepseek"));
    cfg.set("backend_base_url", json!(llm.url("")));
    cfg.env
        .insert("DEEPSEEK_API_KEY".into(), "fake-llm-key".into());
    cfg.set("_server_tools", json!([]));
    cfg.set(
        "tools",
        json!([{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]),
    );
    let mut handle = engine::spawn(cfg);
    handle
        .commands
        .send(EngineCommand::UserText("first query".into()))
        .await
        .unwrap();
    handle.commands.send(EngineCommand::Generate).await.unwrap();
    let turn = engine_until(&mut handle, "golive.turn.metrics").await;
    let call = turn
        .iter()
        .find(|event| event["event"]["type"] == "response.output_item.done")
        .expect("client receives a completed function call");
    assert_eq!(call["event"]["item"]["call_id"], "external-0");
    requests.recv().await.unwrap();
    handle.commands.send(EngineCommand::Generate).await.unwrap();
    let error = engine_until(&mut handle, "error").await;
    assert_eq!(
        error.last().unwrap()["error"]["code"],
        "pending_tool_results"
    );
    handle
        .commands
        .send(EngineCommand::ToolResult {
            id: "unknown".into(),
            output: "bad".into(),
        })
        .await
        .unwrap();
    let error = engine_until(&mut handle, "error").await;
    assert_eq!(error.last().unwrap()["error"]["code"], "unknown_tool_call");
    handle
        .commands
        .send(EngineCommand::AppendThinking("auxiliary context".into()))
        .await
        .unwrap();
    handle
        .commands
        .send(EngineCommand::Speak("Please wait.".into()))
        .await
        .unwrap();
    engine_until(&mut handle, "golive.turn.metrics").await;
    handle
        .commands
        .send(EngineCommand::ToolResult {
            id: "external-0".into(),
            output: "good".into(),
        })
        .await
        .unwrap();
    handle.commands.send(EngineCommand::Generate).await.unwrap();
    engine_until(&mut handle, "golive.turn.metrics").await;
    let second = requests.recv().await.unwrap();
    let messages = second["messages"].as_array().unwrap();
    let call = messages
        .iter()
        .position(|message| message["tool_calls"][0]["id"] == "external-0")
        .unwrap();
    assert_eq!(
        messages[call + 1]["tool_call_id"],
        "external-0",
        "auxiliary context and speech must not split the tool exchange: {messages:?}"
    );
    assert!(
        messages
            .iter()
            .any(|message| message["content"] == "auxiliary context")
    );
    handle
        .commands
        .send(EngineCommand::ToolResult {
            id: "external-0".into(),
            output: "duplicate".into(),
        })
        .await
        .unwrap();
    let error = engine_until(&mut handle, "error").await;
    assert_eq!(error.last().unwrap()["error"]["code"], "unknown_tool_call");
    handle
        .commands
        .send(EngineCommand::UserText("second query".into()))
        .await
        .unwrap();
    handle.commands.send(EngineCommand::Generate).await.unwrap();
    engine_until(&mut handle, "golive.turn.metrics").await;
    requests.recv().await.unwrap();
    handle
        .commands
        .send(EngineCommand::AppendThinking("before superseding".into()))
        .await
        .unwrap();
    handle
        .commands
        .send(EngineCommand::UserText("superseding query".into()))
        .await
        .unwrap();
    handle.commands.send(EngineCommand::Generate).await.unwrap();
    engine_until(&mut handle, "golive.turn.metrics").await;
    let fourth = requests.recv().await.unwrap();
    let messages = fourth["messages"].as_array().unwrap();
    let tool = messages
        .iter()
        .position(|m| m["tool_calls"][0]["id"] == "external-2")
        .unwrap();
    assert_eq!(messages[tool + 1]["tool_call_id"], "external-2");
    assert!(
        messages[tool + 1]["content"]
            .as_str()
            .unwrap()
            .contains("superseded")
    );
    assert_eq!(messages.last().unwrap()["role"], "user");
    assert_eq!(messages.last().unwrap()["content"], "superseding query");
    handle.cancel.cancel();
}
