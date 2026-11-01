use axum::{
    Json, Router,
    body::{Body, Bytes},
    extract::State,
    response::Response,
    routing::post,
};
use golive_rs::{
    config::Config,
    engine::{self, EngineCommand},
};
use serde_json::{Value, json};
use std::{
    convert::Infallible,
    sync::{
        Arc,
        atomic::{AtomicUsize, Ordering},
    },
    time::Duration,
};
use tokio::{net::TcpListener, sync::mpsc, time::timeout};

fn config() -> Config {
    let mut cfg = Config::default();
    cfg.set("duplex.playback_chunk_ms", json!(20));
    cfg.set("duplex.playback_paced", json!(true));
    cfg.set("duplex.phrase_cache", json!(false));
    cfg.set("vad.line_fill_ms", json!(0));
    cfg
}
async fn until(handle: &mut engine::EngineHandle, kind: &str) -> Vec<Value> {
    timeout(Duration::from_secs(10), async {
        let mut events = Vec::new();
        while let Some(event) = handle.events.recv().await {
            let end = event["type"] == kind;
            events.push(event);
            if end {
                return events;
            }
        }
        panic!("engine stopped before {kind}");
    })
    .await
    .unwrap()
}
fn transcript(events: &[Value]) -> String {
    events
        .iter()
        .filter(|e| e["type"] == "session.output_transcript.delta")
        .filter_map(|e| e["delta"].as_str())
        .collect()
}

#[tokio::test]
async fn transcript_progress_follows_audio_and_completed_text_is_exact_once() {
    let text = "你好，我正在播报测试内容。";
    let mut engine = engine::spawn(config());
    engine
        .commands
        .send(EngineCommand::Speak(text.into()))
        .await
        .unwrap();
    let first = until(&mut engine, "session.output_audio.delta").await;
    assert!(
        transcript(&first).is_empty(),
        "unplayed text was announced before audio"
    );
    let completed = until(&mut engine, "golive.turn.metrics").await;
    assert_eq!(transcript(&completed), text);
    assert!(
        completed
            .iter()
            .filter(|e| e["type"] == "session.output_transcript.delta")
            .count()
            > 1
    );
    engine.cancel.cancel();
}

async fn record_request(
    State(tx): State<mpsc::Sender<Value>>,
    Json(body): Json<Value>,
) -> Response {
    tx.send(body).await.unwrap();
    Response::builder()
        .header("content-type", "text/event-stream")
        .body(Body::from(
            "data: {\"choices\":[{\"delta\":{\"content\":\"好的。\"}}]}\n\ndata: [DONE]\n\n",
        ))
        .unwrap()
}
#[tokio::test]
async fn interrupted_segment_keeps_only_its_heard_prefix_in_transcript_and_next_request() {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let (tx, mut requests) = mpsc::channel(2);
    let router = Router::new()
        .route("/v1/chat/completions", post(record_request))
        .with_state(tx);
    let server = tokio::spawn(async move {
        axum::serve(listener, router).await.unwrap();
    });
    let mut cfg = config();
    cfg.set("llm", json!("openai-compatible"));
    cfg.set("backend_base_url", json!(format!("http://{address}")));
    cfg.env.insert("OPENAI_API_KEY".into(), "fixture".into());
    let text = "这是一个需要播放很久的句子，后面的这段话在打断之后不应该出现在对话历史中。";
    let mut engine = engine::spawn(cfg);
    engine
        .commands
        .send(EngineCommand::Speak(text.into()))
        .await
        .unwrap();
    let mut events = until(&mut engine, "session.output_transcript.delta").await;
    let prefix = transcript(&events);
    assert!(!prefix.is_empty());
    assert!(text.starts_with(&prefix));
    assert!(prefix.chars().count() < text.chars().count());
    engine.commands.send(EngineCommand::Cancel).await.unwrap();
    events.extend(until(&mut engine, "golive.output_audio.truncated").await);
    let heard = transcript(&events);
    let truncated = events.last().unwrap()["text"].as_str().unwrap();
    assert_eq!(truncated, heard);
    assert!(text.starts_with(&heard));
    assert_ne!(heard, text);
    engine
        .commands
        .send(EngineCommand::UserText("继续".into()))
        .await
        .unwrap();
    engine.commands.send(EngineCommand::Generate).await.unwrap();
    let request = timeout(Duration::from_secs(3), requests.recv())
        .await
        .unwrap()
        .unwrap();
    let assistant: Vec<_> = request["messages"]
        .as_array()
        .unwrap()
        .iter()
        .filter(|m| m["role"] == "assistant")
        .collect();
    assert_eq!(assistant.len(), 1);
    assert_eq!(assistant[0]["content"], heard);
    engine.cancel.cancel();
    server.abort();
}

#[derive(Clone)]
struct StallingBackend {
    started: Arc<AtomicUsize>,
    closed: Arc<AtomicUsize>,
}
struct ClosedOnDrop(Arc<AtomicUsize>);
impl Drop for ClosedOnDrop {
    fn drop(&mut self) {
        self.0.fetch_add(1, Ordering::SeqCst);
    }
}
async fn endless_response(State(state): State<StallingBackend>) -> Response {
    state.started.fetch_add(1, Ordering::SeqCst);
    let stream = futures_util::stream::unfold(
        (false, ClosedOnDrop(state.closed)),
        |(sent, guard)| async move {
            let bytes = if !sent {
                let text = "这是用于验证取消的长句子。".repeat(80);
                Bytes::from(format!(
                    "data: {}\n\n",
                    json!({"choices":[{"delta":{"content":text}}]})
                ))
            } else {
                tokio::time::sleep(Duration::from_millis(10)).await;
                Bytes::from_static(b": heartbeat\n\n")
            };
            Some((Ok::<_, Infallible>(bytes), (true, guard)))
        },
    );
    Response::builder()
        .header("content-type", "text/event-stream")
        .body(Body::from_stream(stream))
        .unwrap()
}
#[tokio::test]
async fn cancellation_closes_hedged_backend_streams_while_tts_and_segment_queue_wait() {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let state = StallingBackend {
        started: Arc::new(AtomicUsize::new(0)),
        closed: Arc::new(AtomicUsize::new(0)),
    };
    let router = Router::new()
        .route("/v1/chat/completions", post(endless_response))
        .with_state(state.clone());
    let server = tokio::spawn(async move {
        axum::serve(listener, router).await.unwrap();
    });
    let mut cfg = config();
    cfg.set("llm", json!("openai-compatible"));
    cfg.set("backend_base_url", json!(format!("http://{address}")));
    cfg.set("llm_hedge", json!(2));
    cfg.set("duplex.stream_first_chunk_chars", json!(2));
    cfg.set("duplex.stream_min_chunk_chars", json!(4));
    cfg.set("duplex.stream_max_chunk_chars", json!(8));
    cfg.env.insert("OPENAI_API_KEY".into(), "fixture".into());
    cfg.env
        .insert("GOLIVE_MOCK_TTS_TTFB_MS".into(), "10000".into());
    let mut engine = engine::spawn(cfg);
    engine
        .commands
        .send(EngineCommand::UserText("开始".into()))
        .await
        .unwrap();
    engine.commands.send(EngineCommand::Generate).await.unwrap();
    until(&mut engine, "response.event").await;
    // Give the producer a chance to fill its bounded segment queue while TTS waits.
    tokio::time::sleep(Duration::from_millis(50)).await;
    engine.commands.send(EngineCommand::Cancel).await.unwrap();
    timeout(Duration::from_secs(3), async {
        loop {
            let started = state.started.load(Ordering::SeqCst);
            if started > 0 && state.closed.load(Ordering::SeqCst) >= started {
                break;
            }
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
    })
    .await
    .expect("backend work survived cancelled playback");
    while let Ok(event) = engine.events.try_recv() {
        assert_ne!(event["type"], "session.output_audio.delta");
        assert_ne!(event["type"], "session.output_transcript.delta");
    }
    engine.cancel.cancel();
    server.abort();
}

#[derive(Clone)]
struct ToolBackend {
    requests: mpsc::Sender<Value>,
    local_calls: Arc<AtomicUsize>,
}
async fn tool_response(State(state): State<ToolBackend>, Json(body): Json<Value>) -> Response {
    let has_results = body["messages"]
        .as_array()
        .unwrap()
        .iter()
        .any(|m| m["role"] == "tool");
    state.requests.send(body).await.unwrap();
    let delta = if has_results {
        json!({"choices":[{"delta":{"content":"结果已确认。"},"finish_reason":"stop"}]})
    } else {
        json!({"choices":[{"delta":{"tool_calls":[
            {"index":0,"id":"local-1","type":"function","function":{"name":"local_quote","arguments":"{}"}},
            {"index":1,"id":"external-1","type":"function","function":{"name":"external_lookup","arguments":"{\"query\":\"fixture\"}"}}
        ]},"finish_reason":"tool_calls"}]})
    };
    Response::builder()
        .header("content-type", "text/event-stream")
        .body(Body::from(format!("data: {delta}\n\ndata: [DONE]\n\n")))
        .unwrap()
}
async fn local_tool(State(state): State<ToolBackend>, Json(body): Json<Value>) -> Json<Value> {
    assert_eq!(body["call_id"], "local-1");
    state.local_calls.fetch_add(1, Ordering::SeqCst);
    Json(json!({"quote":42}))
}

#[tokio::test]
async fn mixed_local_and_external_tools_wait_for_results_and_preserve_valid_history() {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let (requests_tx, mut requests) = mpsc::channel(4);
    let local_calls = Arc::new(AtomicUsize::new(0));
    let state = ToolBackend {
        requests: requests_tx,
        local_calls: local_calls.clone(),
    };
    let router = Router::new()
        .route("/v1/chat/completions", post(tool_response))
        .route("/tool", post(local_tool))
        .with_state(state);
    let server = tokio::spawn(async move { axum::serve(listener, router).await.unwrap() });
    let mut cfg = config();
    cfg.set("llm", json!("openai-compatible"));
    cfg.set("backend_base_url", json!(format!("http://{address}")));
    cfg.env.insert("OPENAI_API_KEY".into(), "fixture".into());
    cfg.set("tools", json!([
        {"type":"function","name":"local_quote","url":format!("http://{address}/tool"),"parameters":{"type":"object"}},
        {"type":"function","name":"external_lookup","parameters":{"type":"object","properties":{"query":{"type":"string"}}}}
    ]));
    let mut engine = engine::spawn(cfg);
    engine
        .commands
        .send(EngineCommand::UserText("查询".into()))
        .await
        .unwrap();
    engine.commands.send(EngineCommand::Generate).await.unwrap();
    let function = timeout(Duration::from_secs(5), async {
        loop {
            let event = engine.events.recv().await.unwrap();
            assert_ne!(event["type"], "error", "{event}");
            if event["event"]["type"] == "response.output_item.done" {
                break event;
            }
        }
    })
    .await
    .unwrap();
    assert_eq!(function["event"]["item"]["call_id"], "external-1");
    assert_eq!(function["event"]["item"]["name"], "external_lookup");
    assert_eq!(function["delegation_id"], function["event"]["response_id"]);
    assert_eq!(local_calls.load(Ordering::SeqCst), 1);
    let _initial_request = requests.recv().await.unwrap();
    assert!(
        requests.try_recv().is_err(),
        "LLM continued before client supplied its result"
    );
    engine.commands.send(EngineCommand::Generate).await.unwrap();
    let pending = until(&mut engine, "error").await;
    assert_eq!(
        pending.last().unwrap()["error"]["code"],
        "pending_tool_results"
    );

    // The public function event must never overtake its pending history entry.
    engine
        .commands
        .send(EngineCommand::ToolResult {
            id: "external-1".into(),
            output: r#"{"result":"available"}"#.into(),
        })
        .await
        .unwrap();
    engine.commands.send(EngineCommand::Generate).await.unwrap();
    let continued = timeout(Duration::from_secs(5), requests.recv())
        .await
        .unwrap()
        .unwrap();
    let messages = continued["messages"].as_array().unwrap();
    let assistant = messages.iter().find(|m| m["role"] == "assistant").unwrap();
    assert_eq!(assistant["tool_calls"].as_array().unwrap().len(), 2);
    let results: Vec<_> = messages.iter().filter(|m| m["role"] == "tool").collect();
    assert_eq!(results.len(), 2);
    assert_eq!(results[0]["tool_call_id"], "local-1");
    assert_eq!(results[0]["content"], r#"{"quote":42}"#);
    assert_eq!(results[1]["tool_call_id"], "external-1");
    assert_eq!(results[1]["content"], r#"{"result":"available"}"#);
    let completed = until(&mut engine, "golive.turn.metrics").await;
    assert!(completed.iter().all(|e| e["type"] != "error"));
    assert_eq!(transcript(&completed), "结果已确认。");
    assert_eq!(local_calls.load(Ordering::SeqCst), 1);
    engine.cancel.cancel();
    server.abort();
}
