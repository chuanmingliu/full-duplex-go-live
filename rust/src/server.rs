//! HTTP/WebSocket transport. All voice work runs in the native engine.
mod session;
use crate::{
    config::Config,
    engine::{self, EngineCommand, EngineHandle},
    protocol::{self, AudioFormat, Realtime},
};
use anyhow::{Context, Result, bail};
use axum::{
    Json, Router,
    extract::{
        ConnectInfo, DefaultBodyLimit, Query, State, WebSocketUpgrade,
        ws::{Message, WebSocket},
    },
    http::{HeaderMap, StatusCode},
    response::{IntoResponse, Response},
    routing::{any, get, post},
};
use base64::{Engine as _, engine::general_purpose::STANDARD};
use serde_json::{Value, json};
use session::run_socket;
use std::{
    collections::HashMap,
    net::SocketAddr,
    sync::{
        Arc, Mutex,
        atomic::{AtomicBool, AtomicU64, Ordering},
    },
    time::{Duration, Instant},
};
use tokio::sync::{mpsc, oneshot};
use tokio_util::sync::CancellationToken;

#[derive(Clone)]
struct App {
    config: Config,
    sessions: Arc<Mutex<HashMap<String, Entry>>>,
    accepted: Arc<AtomicU64>,
    openings: Arc<Mutex<HashMap<String, WarmState>>>,
    shutdown: CancellationToken,
}

#[derive(Clone, Default)]
struct WarmState {
    ready: bool,
    bytes: usize,
    failed: bool,
}

struct Entry {
    control: mpsc::Sender<Control>,
    created: Instant,
    realtime: bool,
    call: String,
    ip: String,
    opened: Arc<AtomicBool>,
    started: Arc<AtomicBool>,
}

enum Control {
    Open {
        greeting: String,
        reply: oneshot::Sender<bool>,
    },
}

struct Registration {
    app: App,
    id: String,
}
impl Drop for Registration {
    fn drop(&mut self) {
        self.app.sessions.lock().unwrap().remove(&self.id);
    }
}

pub fn router(config: Config) -> Router {
    build_router(config, CancellationToken::new())
}

fn build_router(config: Config, shutdown: CancellationToken) -> Router {
    let app = App {
        config: config.clone(),
        sessions: Arc::default(),
        accepted: Arc::default(),
        openings: Arc::default(),
        shutdown,
    };
    let mut routes = Router::new()
        .route("/livez", get(livez))
        .route("/healthz", get(livez))
        .route("/readyz", get(readyz))
        .route("/metrics", get(metrics))
        .route("/v1/live", get(live))
        .route("/v1/live/sessions", get(live))
        .route("/v1/live/providers", get(public_config))
        .route("/v1/live/config", get(public_config))
        .route("/v1/live/sessions/{id}/attach", any(unsupported))
        .route("/v1/live/sessions/{id}/fork", any(unsupported))
        .route("/v1/live/sessions/{id}/content", any(unsupported));
    if config.bool("realtime.enabled", true) {
        routes = routes
            .route("/v1/realtime", get(realtime))
            .route("/v1/opening", post(opening).put(opening))
            .route("/v1/pool", get(pool));
    }
    let web_root = config.string("web_root", "../web");
    if !web_root.is_empty() {
        routes = routes.fallback_service(tower_http::services::ServeDir::new(web_root));
    }
    routes
        .layer(DefaultBodyLimit::max(16 * 1024))
        .with_state(app)
}

pub async fn serve(config: Config, shutdown: CancellationToken) -> Result<()> {
    config.validate()?;
    if config.bool("auth_required", false) && auth_token(&config).is_empty() {
        bail!("auth_required needs GOLIVE_AUTH_TOKEN");
    }
    let configured = config.string("addr", "127.0.0.1:8766");
    let addr = configured
        .strip_prefix(':')
        .map(|port| format!("0.0.0.0:{port}"))
        .unwrap_or(configured);
    let listener = tokio::net::TcpListener::bind(&addr)
        .await
        .with_context(|| format!("binding {addr}"))?;
    tracing::info!(%addr, "Rust golive service listening");
    axum::serve(
        listener,
        build_router(config, shutdown.clone()).into_make_service_with_connect_info::<SocketAddr>(),
    )
    .with_graceful_shutdown(shutdown.cancelled_owned())
    .await?;
    Ok(())
}

fn failure(status: StatusCode, code: &str) -> Response {
    (status, Json(json!({"ok":false,"error":code}))).into_response()
}
fn auth_token(config: &Config) -> String {
    let env = config.secret("GOLIVE_AUTH_TOKEN");
    if env.is_empty() {
        config.string("auth_token", "")
    } else {
        env
    }
}

fn provided_token(headers: &HeaderMap, query: &HashMap<String, String>) -> String {
    if let Some(value) = headers.get("authorization").and_then(|v| v.to_str().ok()) {
        if let Some((scheme, token)) = value.split_once(' ') {
            if scheme.eq_ignore_ascii_case("bearer") {
                return token.trim().into();
            }
        }
    }
    if let Some(token) = query.get("token") {
        return token.clone();
    }
    headers
        .get("sec-websocket-protocol")
        .and_then(|v| v.to_str().ok())
        .unwrap_or("")
        .split(',')
        .find_map(|v| {
            v.trim()
                .strip_prefix("openai-insecure-api-key.")
                .or_else(|| v.trim().strip_prefix("golive.auth."))
        })
        .unwrap_or("")
        .into()
}

fn equal_token(got: &str, want: &str) -> bool {
    let mut mismatch = got.len() ^ want.len();
    for i in 0..got.len().max(want.len()) {
        mismatch |= usize::from(
            got.as_bytes().get(i).copied().unwrap_or(0)
                ^ want.as_bytes().get(i).copied().unwrap_or(0),
        );
    }
    mismatch == 0
}

fn authorized(app: &App, headers: &HeaderMap, query: &HashMap<String, String>) -> bool {
    let want = auth_token(&app.config);
    if want.is_empty() {
        return !app.config.bool("auth_required", false);
    }
    equal_token(&provided_token(headers, query), &want)
}

fn origin_allowed(app: &App, headers: &HeaderMap) -> bool {
    let Some(origin) = headers.get("origin").and_then(|v| v.to_str().ok()) else {
        return true;
    };
    if let Some(allowed) = app
        .config
        .value("allowed_origins")
        .and_then(Value::as_array)
        .filter(|a| !a.is_empty())
    {
        return allowed
            .iter()
            .filter_map(Value::as_str)
            .any(|s| s == "*" || s == origin);
    }
    if auth_token(&app.config).is_empty() && !app.config.bool("auth_required", false) {
        return true;
    }
    let Ok(url) = url::Url::parse(origin) else {
        return false;
    };
    let Some(host) = headers.get("host").and_then(|v| v.to_str().ok()) else {
        return false;
    };
    url[url::Position::BeforeHost..url::Position::AfterPort] == *host
}

async fn livez() -> Json<Value> {
    Json(json!({"ok":true,"service":"golive-rs"}))
}
async fn readyz(State(app): State<App>) -> Response {
    let active = app.sessions.lock().unwrap().len();
    let max = app.config.usize("max_sessions", 64);
    let ready = !app.shutdown.is_cancelled() && (max == 0 || active < max);
    (
        if ready {
            StatusCode::OK
        } else {
            StatusCode::SERVICE_UNAVAILABLE
        },
        Json(json!({"ok":ready,"active_sessions":active,"max_sessions":max})),
    )
        .into_response()
}

async fn metrics(
    State(app): State<App>,
    headers: HeaderMap,
    Query(query): Query<HashMap<String, String>>,
) -> Response {
    if !app.config.bool("enable_metrics", true) {
        return failure(StatusCode::NOT_FOUND, "not_found");
    }
    let token = app.config.secret("GOLIVE_METRICS_TOKEN");
    if !app.config.bool("metrics_public", false)
        && !authorized(&app, &headers, &query)
        && (token.is_empty() || !equal_token(&provided_token(&headers, &query), &token))
    {
        return failure(StatusCode::UNAUTHORIZED, "unauthorized");
    }
    let active = app.sessions.lock().unwrap().len();
    ([("content-type","text/plain; version=0.0.4")], format!("# TYPE golive_active_sessions gauge\ngolive_active_sessions {active}\n# TYPE golive_sessions_total counter\ngolive_sessions_total {}\n", app.accepted.load(Ordering::Relaxed))).into_response()
}

async fn public_config(
    State(app): State<App>,
    headers: HeaderMap,
    Query(query): Query<HashMap<String, String>>,
) -> Response {
    if !authorized(&app, &headers, &query) {
        return failure(StatusCode::UNAUTHORIZED, "unauthorized");
    }
    Json(json!({"service":"golive-rs","model":app.config.string("model","gpt-live-1"),
        "asr":["mock","tencent"],"llm":["mock","deepseek","openai-compatible","cerebras","inception"],"tts":["mock","minimax"],
        "selected":{"asr":app.config.string("asr","mock"),"llm":app.config.string("llm","mock"),"tts":app.config.string("tts","mock")},
        "defaults":{"instructions":app.config.string("instructions",""),"greeting":app.config.string("greeting",""),
            "on_new_query":app.config.string("duplex.on_new_query","cut"),"user_backchannel_phrases":app.config.value("duplex.user_backchannel_phrases"),
            "model":app.config.string("model","golive-1"),"language":app.config.string("language","zh"),"rate":app.config.usize("client_rate",24000)},
        "client_rate":app.config.usize("client_rate",24000),"allow_provider_override":app.config.bool("allow_provider_override",false)})).into_response()
}

async fn unsupported(
    State(app): State<App>,
    headers: HeaderMap,
    Query(query): Query<HashMap<String, String>>,
) -> Response {
    if !authorized(&app, &headers, &query) {
        return failure(StatusCode::UNAUTHORIZED, "unauthorized");
    }
    failure(
        StatusCode::NOT_IMPLEMENTED,
        "session_storage_and_sidebands_not_supported",
    )
}

async fn pool(
    State(app): State<App>,
    headers: HeaderMap,
    Query(query): Query<HashMap<String, String>>,
) -> Response {
    if !authorized(&app, &headers, &query) {
        return failure(StatusCode::UNAUTHORIZED, "unauthorized");
    }
    let sessions: Vec<_> = app
        .sessions
        .lock()
        .unwrap()
        .iter()
        .map(|(id, e)| {
            json!({"id":id,
        "protocol":if e.realtime {"openai-realtime"} else {"gpt-live-1"},"call":e.call,
        "opened":e.opened.load(Ordering::Relaxed),"age_ms":e.created.elapsed().as_millis()})
        })
        .collect();
    Json(
        json!({"ok":true,"service":"golive-rs","in_use":sessions.len(),"sessions":sessions,
        "capacity":app.config.usize("max_sessions",64),"realtime":app.config.value("realtime")}),
    )
    .into_response()
}

async fn opening(
    State(app): State<App>,
    headers: HeaderMap,
    Query(query): Query<HashMap<String, String>>,
    Json(body): Json<Value>,
) -> Response {
    if !authorized(&app, &headers, &query) {
        return failure(StatusCode::UNAUTHORIZED, "unauthorized");
    }
    let greeting = body["greeting"]
        .as_str()
        .or_else(|| body["text"].as_str())
        .unwrap_or("");
    if greeting.chars().count() > 400 {
        return failure(StatusCode::BAD_REQUEST, "greeting_too_long");
    }
    let call = ["callId", "call_id", "call"]
        .iter()
        .find_map(|key| {
            body[*key]
                .as_str()
                .or_else(|| query.get(*key).map(String::as_str))
        })
        .unwrap_or("");
    if body["warm"].as_bool().unwrap_or(false) {
        let state = {
            let mut openings = app.openings.lock().unwrap();
            if let Some(state) = openings.get(greeting).cloned() {
                if state.failed {
                    openings.remove(greeting);
                }
                state
            } else if greeting.is_empty() {
                WarmState {
                    ready: true,
                    ..Default::default()
                }
            } else {
                if openings.len() >= 64 {
                    openings.retain(|_, v| !v.ready && !v.failed);
                }
                if openings.len() >= 64 {
                    return failure(StatusCode::TOO_MANY_REQUESTS, "opening_cache_busy");
                }
                openings.insert(greeting.into(), WarmState::default());
                let work = app.clone();
                let text = greeting.to_owned();
                tokio::spawn(async move {
                    let cancel = work.shutdown.child_token();
                    let result = tokio::time::timeout(
                        Duration::from_secs(20),
                        engine::warm_speech(work.config.clone(), text.clone(), cancel.clone()),
                    )
                    .await;
                    cancel.cancel();
                    let state = match result {
                        Ok(Ok(bytes)) => WarmState {
                            ready: true,
                            bytes,
                            failed: false,
                        },
                        _ => WarmState {
                            failed: true,
                            ..Default::default()
                        },
                    };
                    work.openings.lock().unwrap().insert(text, state);
                });
                WarmState::default()
            }
        };
        let mut result = json!({"ok":!state.failed,"callId":call,"warm":true,"kicked":[],
            "cached":[{"ready":state.ready,"bytes":state.bytes,"callId":call}]});
        if state.failed {
            result["error"] = json!("opening_synthesis_failed");
        }
        return Json(result).into_response();
    }
    let targets: Vec<_> = app
        .sessions
        .lock()
        .unwrap()
        .iter()
        .filter(|(id, e)| {
            e.realtime
                && e.started.load(Ordering::Relaxed)
                && !e.opened.load(Ordering::Relaxed)
                && (call.is_empty() || call == e.call || call == id.as_str())
        })
        .map(|(id, e)| {
            (
                if e.call.is_empty() {
                    id.clone()
                } else {
                    e.call.clone()
                },
                e.control.clone(),
            )
        })
        .collect();
    let mut kicked = vec![];
    for (id, control) in targets {
        let (reply, done) = oneshot::channel();
        if control
            .try_send(Control::Open {
                greeting: greeting.into(),
                reply,
            })
            .is_ok()
            && matches!(
                tokio::time::timeout(Duration::from_secs(2), done).await,
                Ok(Ok(true))
            )
        {
            kicked.push(id);
        }
    }
    Json(json!({"ok":true,"callId":call,"warm":false,"kicked":kicked,"cached":[]})).into_response()
}

async fn live(
    State(app): State<App>,
    headers: HeaderMap,
    Query(query): Query<HashMap<String, String>>,
    peer: std::result::Result<
        ConnectInfo<SocketAddr>,
        axum::extract::rejection::ExtensionRejection,
    >,
    ws: WebSocketUpgrade,
) -> Response {
    upgrade(app, headers, query, peer.ok(), ws, false).await
}
async fn realtime(
    State(app): State<App>,
    headers: HeaderMap,
    Query(query): Query<HashMap<String, String>>,
    peer: std::result::Result<
        ConnectInfo<SocketAddr>,
        axum::extract::rejection::ExtensionRejection,
    >,
    ws: WebSocketUpgrade,
) -> Response {
    upgrade(app, headers, query, peer.ok(), ws, true).await
}

async fn upgrade(
    app: App,
    headers: HeaderMap,
    query: HashMap<String, String>,
    peer: Option<ConnectInfo<SocketAddr>>,
    ws: WebSocketUpgrade,
    realtime: bool,
) -> Response {
    if !authorized(&app, &headers, &query) {
        return failure(StatusCode::UNAUTHORIZED, "unauthorized");
    }
    if !origin_allowed(&app, &headers) {
        return failure(StatusCode::FORBIDDEN, "origin_not_allowed");
    }
    if app.shutdown.is_cancelled() {
        return failure(StatusCode::SERVICE_UNAVAILABLE, "server_shutdown");
    }
    let rate = query
        .get("sample_rate")
        .or_else(|| query.get("rate"))
        .map(|n| n.parse::<u32>())
        .transpose();
    let rate = match rate {
        Ok(Some(n)) => n,
        Ok(None) => app.config.usize(
            if realtime {
                "realtime.sample_rate"
            } else {
                "client_rate"
            },
            24000,
        ) as u32,
        Err(_) => return failure(StatusCode::BAD_REQUEST, "bad_rate"),
    };
    if AudioFormat::new("audio/pcm", rate).is_err() {
        return failure(StatusCode::BAD_REQUEST, "bad_rate");
    }
    let call = ["call", "call_id", "callId"]
        .iter()
        .find_map(|key| query.get(*key))
        .cloned()
        .unwrap_or_default();
    if call.len() > 128
        || !call
            .chars()
            .all(|c| c.is_ascii_alphanumeric() || "-_:.".contains(c))
    {
        return failure(StatusCode::BAD_REQUEST, "bad_call_id");
    }
    let ip = if app.config.bool("trust_proxy", false) {
        headers
            .get("x-forwarded-for")
            .and_then(|v| v.to_str().ok())
            .and_then(|s| s.split(',').next())
            .map(|s| s.trim().to_owned())
    } else {
        None
    }
    .unwrap_or_else(|| {
        peer.map(|p| p.0.ip().to_string())
            .unwrap_or_else(|| "unknown".into())
    });
    let id = format!("sess_{}", uuid::Uuid::new_v4().simple());
    let (control, commands) = mpsc::channel(8);
    let opened = Arc::new(AtomicBool::new(false));
    let started = Arc::new(AtomicBool::new(false));
    {
        let mut sessions = app.sessions.lock().unwrap();
        let maximum = app.config.usize("max_sessions", 64);
        let per_ip = app.config.usize("max_sessions_per_ip", 0);
        if maximum > 0 && sessions.len() >= maximum {
            return failure(StatusCode::SERVICE_UNAVAILABLE, "capacity_exceeded");
        }
        if per_ip > 0 && sessions.values().filter(|e| e.ip == ip).count() >= per_ip {
            return failure(StatusCode::TOO_MANY_REQUESTS, "per_ip_capacity_exceeded");
        }
        sessions.insert(
            id.clone(),
            Entry {
                control,
                created: Instant::now(),
                realtime,
                call,
                ip,
                opened: opened.clone(),
                started: started.clone(),
            },
        );
    }
    app.accepted.fetch_add(1, Ordering::Relaxed);
    let registration = Registration {
        app: app.clone(),
        id: id.clone(),
    };
    ws.max_message_size(1024 * 1024)
        .max_frame_size(1024 * 1024)
        .protocols(["realtime", "gpt-live-1"])
        .on_upgrade(move |socket| {
            run_socket(
                socket,
                app,
                query,
                id,
                rate,
                realtime,
                commands,
                opened,
                started,
                registration,
            )
        })
}
