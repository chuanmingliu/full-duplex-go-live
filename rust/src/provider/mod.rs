//! Streaming provider contracts. Every producer stops when its consumer is dropped.
mod deepseek;
mod fallback;
mod fish;
mod minimax;
mod mock;
mod tencent;

use crate::config::Config;
use anyhow::{Result, bail};
use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::time::Duration;
use tokio::sync::mpsc;
use tokio_util::sync::CancellationToken;

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
pub struct Message {
    pub role: String,
    pub content: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub tool_call_id: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub tool_calls: Vec<ToolCall>,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize, PartialEq, Eq)]
pub struct ToolCall {
    pub id: String,
    pub name: String,
    pub arguments: String,
}

#[derive(Clone, Debug, Default)]
pub struct LlmDelta {
    pub text: String,
    pub tool_call: Option<ToolCall>,
    pub usage: Option<Value>,
}

#[derive(Debug)]
pub enum AsrCommand {
    Audio(Vec<u8>),
    Finish,
    Close,
}

#[derive(Clone, Debug)]
pub struct AsrResult {
    pub text: String,
    pub final_result: bool,
}

pub struct AsrSession {
    pub commands: mpsc::Sender<AsrCommand>,
    pub results: mpsc::Receiver<Result<AsrResult>>,
}

/// Validate selected adapter names and credential presence without contacting vendors.
pub fn preflight(cfg: &Config) -> Result<()> {
    match cfg.string("asr", "mock").as_str() {
        "mock" => {}
        "tencent" => {
            for key in [
                "TENCENT_ASR_APP_ID",
                "TENCENT_ASR_SECRET_ID",
                "TENCENT_ASR_SECRET_KEY",
            ] {
                required(cfg, key)?;
            }
        }
        name => bail!("unsupported Rust ASR provider {name:?}; supported: mock, tencent"),
    }
    match cfg.string("llm", "mock").as_str() {
        "mock" => {}
        "deepseek" => {
            required(cfg, "DEEPSEEK_API_KEY")?;
        }
        "openai-compatible" => {
            required(cfg, "OPENAI_API_KEY")?;
        }
        "cerebras" => {
            required(cfg, "CEREBRAS_API_KEY")?;
        }
        "inception" => {
            required(cfg, "INCEPTION_API_KEY")?;
        }
        name => bail!(
            "unsupported Rust LLM provider {name:?}; supported: mock, deepseek, openai-compatible, cerebras, inception"
        ),
    }
    validate_tts(cfg)?;
    let fallback = cfg.string("tts_fallback", "");
    if !fallback.is_empty() && cfg.usize("tts_fallback_after_ms", 0) > 0 {
        let mut secondary = cfg.clone();
        secondary.set("tts", Value::String(fallback.clone()));
        secondary.set("voice", Value::String(String::new()));
        if let Err(error) = validate_tts(&secondary) {
            tracing::warn!(provider=%fallback, %error,"TTS fallback unavailable; primary remains enabled");
        }
    }
    if cfg.usize("tts_hedge", 1) > 1 || cfg.usize("tts_account_rpm", 0) > 0 {
        tracing::warn!(
            "Rust TTS account-pool hedging and shared RPM allocation are not implemented; primary and cross-vendor fallback are active"
        );
    }
    for (provider, key) in [
        ("minimax", "MINIMAX_TTS_API_KEY"),
        ("fish", "FISH_TTS_API_KEY"),
    ] {
        if (cfg.string("tts", "mock") == provider || fallback == provider)
            && api_keys(cfg, key).is_ok_and(|keys| keys.len() > 1)
        {
            tracing::warn!(%provider,"Rust TTS uses the first configured account; account rotation and refusal cooldown are not implemented");
        }
    }
    Ok(())
}

fn validate_tts(cfg: &Config) -> Result<()> {
    match cfg.string("tts", "mock").as_str() {
        "mock" => {}
        "minimax" => {
            required(cfg, "MINIMAX_TTS_API_KEY")?;
            if voice(cfg).is_empty() {
                bail!("MiniMax requires MINIMAX_TTS_VOICE_ID or voice in the profile");
            }
        }
        "fish" => {
            required(cfg, "FISH_TTS_API_KEY")?;
        }
        name => bail!("unsupported Rust TTS provider {name:?}; supported: mock, minimax, fish"),
    }
    Ok(())
}

/// Open pooled LLM connections without generating tokens. Failure is best-effort.
pub async fn prewarm(cfg: Config, cancel: CancellationToken) -> Result<()> {
    if cfg.string("llm", "mock") == "mock" {
        return Ok(());
    }
    deepseek::prewarm(cfg, cancel).await
}

pub async fn open_asr(cfg: Config, cancel: CancellationToken) -> Result<AsrSession> {
    match cfg.string("asr", "mock").as_str() {
        "mock" => mock::open_asr(cfg, cancel).await,
        "tencent" => tencent::open_asr(cfg, cancel).await,
        name => bail!("unsupported Rust ASR provider {name:?}"),
    }
}

pub async fn stream_llm(
    cfg: Config,
    messages: Vec<Message>,
    cancel: CancellationToken,
) -> Result<mpsc::Receiver<Result<LlmDelta>>> {
    match cfg.string("llm", "mock").as_str() {
        "mock" => mock::stream_llm(cfg, messages, cancel).await,
        "deepseek" | "openai-compatible" | "cerebras" | "inception" => {
            deepseek::stream_llm(cfg, messages, cancel).await
        }
        name => bail!("unsupported Rust LLM provider {name:?}"),
    }
}

/// Output is always little-endian PCM16, mono, 16 kHz.
pub async fn synthesize(
    cfg: Config,
    text: String,
    cancel: CancellationToken,
) -> Result<mpsc::Receiver<Result<Vec<u8>>>> {
    match cfg.string("tts", "mock").as_str() {
        "mock" => mock::synthesize(cfg, text, cancel).await,
        "minimax" => minimax::synthesize(cfg, text, cancel).await,
        "fish" => fish::synthesize(cfg, text, cancel).await,
        name => bail!("unsupported Rust TTS provider {name:?}"),
    }
}

/// One synthesis task per client session. The manager serializes bounded requests,
/// keeps a clean MiniMax socket warm, and closes it after interruption.
#[derive(Clone)]
pub struct Synthesizer {
    inner: std::sync::Arc<SynthesizerInner>,
    fallback: Option<std::sync::Arc<Synthesizer>>,
    fallback_wait: Duration,
}

struct SynthesizerInner {
    requests: mpsc::Sender<SynthesisRequest>,
    shutdown: CancellationToken,
}

impl Drop for SynthesizerInner {
    fn drop(&mut self) {
        self.shutdown.cancel();
    }
}

struct SynthesisRequest {
    text: String,
    cancel: CancellationToken,
    out: mpsc::Sender<Result<Vec<u8>>>,
}

impl Synthesizer {
    pub fn new(cfg: Config) -> Self {
        let name = cfg.string("tts_fallback", "");
        let wait = Duration::from_millis(cfg.usize("tts_fallback_after_ms", 0).min(60000) as u64);
        let mut primary = Self::new_single(cfg.clone());
        if !name.is_empty() && !wait.is_zero() {
            let mut secondary = cfg;
            secondary.set("tts", Value::String(name));
            secondary.set("tts_fallback", Value::String(String::new()));
            secondary.set("voice", Value::String(String::new()));
            if validate_tts(&secondary).is_ok() {
                primary.fallback = Some(std::sync::Arc::new(Self::new_single(secondary)));
                primary.fallback_wait = wait;
            }
        }
        primary
    }

    fn new_single(cfg: Config) -> Self {
        let (requests, mut queue) = mpsc::channel::<SynthesisRequest>(16);
        let shutdown = CancellationToken::new();
        let worker_shutdown = shutdown.clone();
        tokio::spawn(async move {
            let mut session = minimax::Session::new(cfg.clone());
            let is_minimax = cfg.string("tts", "mock") == "minimax";
            if is_minimax {
                tokio::select! {
                    _ = worker_shutdown.cancelled() => return,
                    result = session.prewarm() => if result.is_err() { session.reset(); },
                }
            }
            let mut keep_alive = tokio::time::interval(Duration::from_secs(1));
            keep_alive.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);
            loop {
                let request = tokio::select! {
                    _ = worker_shutdown.cancelled() => return,
                    _ = keep_alive.tick(), if is_minimax => {
                        tokio::select! {
                            _ = worker_shutdown.cancelled() => return,
                            _ = session.keep_alive() => {},
                        }
                        continue;
                    },
                    request = queue.recv() => match request { Some(r) => r, None => return },
                };
                if request.cancel.is_cancelled() || request.out.is_closed() {
                    continue;
                }
                let run = async {
                    if is_minimax {
                        session.speak(&request.text, &request.out).await
                    } else {
                        let mut source =
                            synthesize(cfg.clone(), request.text.clone(), request.cancel.clone())
                                .await?;
                        while let Some(chunk) = source.recv().await {
                            if request.out.send(chunk).await.is_err() {
                                break;
                            }
                        }
                        Ok(())
                    }
                };
                let result = tokio::select! {
                    _ = worker_shutdown.cancelled() => return,
                    _ = request.cancel.cancelled() => None,
                    _ = request.out.closed() => None,
                    result = run => Some(result),
                };
                match result {
                    Some(Ok(())) => {}
                    Some(Err(error)) => {
                        session.reset();
                        tokio::select! {
                            _ = worker_shutdown.cancelled() => return,
                            _ = request.cancel.cancelled() => {},
                            _ = request.out.send(Err(error)) => {},
                        }
                    }
                    None => session.reset(),
                }
            }
        });
        Self {
            inner: std::sync::Arc::new(SynthesizerInner { requests, shutdown }),
            fallback: None,
            fallback_wait: Duration::ZERO,
        }
    }

    pub async fn synthesize(
        &self,
        text: String,
        cancel: CancellationToken,
    ) -> Result<mpsc::Receiver<Result<Vec<u8>>>> {
        self.synthesize_with_urgency(text, cancel, true).await
    }

    pub async fn synthesize_with_urgency(
        &self,
        text: String,
        cancel: CancellationToken,
        urgent: bool,
    ) -> Result<mpsc::Receiver<Result<Vec<u8>>>> {
        if let Some(secondary) = &self.fallback {
            if text.trim().is_empty() {
                return self.enqueue(text, cancel).await;
            }
            let mut primary = self.clone();
            primary.fallback = None;
            return Ok(fallback::race(
                primary,
                secondary.as_ref().clone(),
                text,
                if urgent {
                    self.fallback_wait
                } else {
                    self.fallback_wait.saturating_mul(3)
                },
                cancel,
            ));
        }
        self.enqueue(text, cancel).await
    }

    async fn enqueue(
        &self,
        text: String,
        cancel: CancellationToken,
    ) -> Result<mpsc::Receiver<Result<Vec<u8>>>> {
        let (out, receiver) = mpsc::channel(16);
        let request = SynthesisRequest {
            text,
            cancel: cancel.clone(),
            out,
        };
        tokio::select! {
            _ = cancel.cancelled() => bail!("synthesis cancelled"),
            result = self.inner.requests.send(request) => result.map_err(|_| anyhow::anyhow!("synthesis session is closed"))?,
        }
        Ok(receiver)
    }
}

fn voice(cfg: &Config) -> String {
    let configured = cfg.string("voice", "");
    if configured.is_empty() {
        cfg.secret("MINIMAX_TTS_VOICE_ID")
    } else {
        configured
    }
}

/// The same ordered, deduplicated account-key inputs accepted by the Go service.
pub fn api_keys(cfg: &Config, name: &str) -> Result<Vec<String>> {
    let mut keys = Vec::new();
    let mut add = |raw: &str| {
        let key = raw.trim().trim_matches(['\'', '"']);
        if !key.is_empty() && !keys.iter().any(|existing| existing == key) {
            keys.push(key.to_string());
        }
    };
    add(&cfg.secret(name));
    for n in 2..=9 {
        add(&cfg.secret(&format!("{name}_{n}")));
    }
    for key in cfg
        .secret(&format!("{name}S"))
        .split(|c: char| c == ',' || c == ';' || c.is_whitespace())
    {
        add(key);
    }
    let file = cfg.secret(&format!("{name}S_FILE"));
    if !file.is_empty() {
        use std::io::Read;
        let mut content = String::new();
        std::fs::File::open(&file)
            .map_err(|_| anyhow::anyhow!("cannot open {name}S_FILE"))?
            .take(1024 * 1024 + 1)
            .read_to_string(&mut content)
            .map_err(|_| anyhow::anyhow!("cannot read {name}S_FILE"))?;
        if content.len() > 1024 * 1024 {
            bail!("{name}S_FILE exceeds 1 MiB");
        }
        for line in content.lines() {
            add(line.split('#').next().unwrap_or(""));
        }
    }
    if keys.is_empty() {
        bail!("missing required credential {name} (or numbered/plural key settings)");
    }
    Ok(keys)
}

fn required(cfg: &Config, name: &str) -> Result<String> {
    if name.ends_with("_API_KEY") {
        return Ok(api_keys(cfg, name)?.remove(0));
    }
    let value = cfg.secret(name);
    if value.trim().is_empty() {
        bail!("missing required credential {name}");
    }
    Ok(value)
}

fn env(cfg: &Config, name: &str, fallback: &str) -> String {
    let value = cfg.secret(name);
    if value.is_empty() {
        fallback.into()
    } else {
        value
    }
}

fn env_number(cfg: &Config, name: &str, fallback: f64) -> f64 {
    cfg.secret(name)
        .parse::<f64>()
        .ok()
        .filter(|n| n.is_finite() && *n >= 0.0)
        .unwrap_or(fallback)
}

fn timeout_seconds(cfg: &Config, name: &str, fallback: f64) -> Duration {
    Duration::from_secs_f64(env_number(cfg, name, fallback).clamp(0.01, 3600.0))
}

async fn delay(cfg: &Config, name: &str, fallback_ms: f64) {
    tokio::time::sleep(Duration::from_secs_f64(
        env_number(cfg, name, fallback_ms).min(3_600_000.0) / 1000.0,
    ))
    .await;
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    use std::collections::HashMap;

    pub(super) fn config(profile: Value) -> Config {
        Config {
            profile,
            env: HashMap::new(),
        }
    }

    #[test]
    fn preflight_accepts_mock_and_rejects_missing_credentials() {
        assert!(preflight(&config(json!({}))).is_ok());
        assert!(
            preflight(&config(json!({"asr":"tencent"})))
                .unwrap_err()
                .to_string()
                .contains("TENCENT_ASR_APP_ID")
        );
        assert!(preflight(&config(json!({"tts":"unknown"}))).is_err());
    }
    #[test]
    fn profile_empty_voice_falls_back_to_secret_voice() {
        let mut cfg = config(json!({"tts":"minimax","voice":""}));
        cfg.env.insert("MINIMAX_TTS_API_KEY".into(), "fake".into());
        cfg.env
            .insert("MINIMAX_TTS_VOICE_ID".into(), "voice-from-env".into());
        assert!(preflight(&cfg).is_ok());
        assert_eq!(voice(&cfg), "voice-from-env");
    }
    #[test]
    fn api_key_sources_preserve_order_and_deduplicate() {
        let mut cfg = config(json!({}));
        cfg.env.extend([
            ("DEEPSEEK_API_KEY".into(), "'first'".into()),
            ("DEEPSEEK_API_KEY_2".into(), "second".into()),
            (
                "DEEPSEEK_API_KEYS".into(),
                "second, third; fourth fifth".into(),
            ),
        ]);
        let path = std::env::temp_dir().join(format!("golive-rs-keys-{}", uuid::Uuid::new_v4()));
        std::fs::write(&path, "third # duplicate\nsixth\n").unwrap();
        cfg.env.insert(
            "DEEPSEEK_API_KEYS_FILE".into(),
            path.to_string_lossy().into_owned(),
        );
        let keys = api_keys(&cfg, "DEEPSEEK_API_KEY").unwrap();
        std::fs::remove_file(path).unwrap();
        assert_eq!(
            keys,
            vec!["first", "second", "third", "fourth", "fifth", "sixth"]
        );
    }
}
