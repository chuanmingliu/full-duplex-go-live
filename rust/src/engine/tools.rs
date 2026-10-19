use super::Internal;
use crate::{config::Config, provider::ToolCall};
use anyhow::{Result, bail};
use serde_json::{Value, json};
use std::{
    collections::HashMap,
    sync::{Arc, Mutex as StdMutex, OnceLock},
    time::Duration,
};
use tokio::{
    sync::{Mutex, Semaphore, mpsc},
    time::Instant,
};
use tokio_util::sync::CancellationToken;

type AudioCache = Arc<Mutex<HashMap<String, Vec<u8>>>>;
static CACHES: OnceLock<StdMutex<HashMap<String, AudioCache>>> = OnceLock::new();
pub(super) fn cache_for(cfg: &Config) -> AudioCache {
    let settings: Vec<_> = cfg
        .env
        .iter()
        .filter(|(k, _)| {
            (k.starts_with("MINIMAX_TTS_") || k.starts_with("FISH_TTS_"))
                && !k.contains("KEY")
                && !k.contains("TOKEN")
        })
        .collect();
    let mut settings = settings;
    settings.sort_by_key(|(key, _)| *key);
    let key = format!(
        "{}|{}|{}|{}|{:?}|{}",
        cfg.string("tts", "mock"),
        cfg.string("voice", ""),
        cfg.f64("speed", 1.0),
        cfg.string("language", "zh"),
        settings,
        cfg.value("tts_options").unwrap_or(&Value::Null)
    );
    let mut caches = CACHES
        .get_or_init(|| StdMutex::new(HashMap::new()))
        .lock()
        .unwrap_or_else(|p| p.into_inner());
    if caches.len() >= 32 && !caches.contains_key(&key) {
        caches.retain(|_, v| Arc::strong_count(v) > 1);
    }
    caches
        .entry(key)
        .or_insert_with(|| Arc::new(Mutex::new(HashMap::new())))
        .clone()
}
#[derive(Clone)]
pub(super) struct Runtime {
    cfg: Config,
    server_tools: Value,
    memory: Arc<Mutex<(u64, Value)>>,
    latest: Arc<Mutex<HashMap<String, String>>>,
    workers: Arc<Semaphore>,
    inner: mpsc::Sender<Internal>,
    cancel: CancellationToken,
    pub audio_cache: AudioCache,
}
impl Runtime {
    pub fn new(cfg: Config, inner: mpsc::Sender<Internal>, cancel: CancellationToken) -> Self {
        let memory = cfg
            .value("memory")
            .cloned()
            .filter(Value::is_object)
            .unwrap_or(json!({}));
        Self {
            server_tools: cfg
                .value("_server_tools")
                .or_else(|| cfg.value("tools"))
                .cloned()
                .unwrap_or(json!([])),
            audio_cache: cache_for(&cfg),
            workers: Arc::new(Semaphore::new(cfg.usize("tools_workers", 4).clamp(1, 32))),
            cfg,
            memory: Arc::new(Mutex::new((0, memory))),
            latest: Arc::new(Mutex::new(HashMap::new())),
            inner,
            cancel,
        }
    }
    pub fn update_config(&mut self, cfg: Config) {
        self.audio_cache = cache_for(&cfg);
        self.cfg = cfg;
    }
    pub fn is_external(&self, call: &ToolCall) -> bool {
        if (call.name == "update_memory" && self.cfg.bool("tools_memory", false))
            || (call.name == "end_call" && self.cfg.bool("tools_end_call", false))
        {
            return false;
        }
        !self.server_tools.as_array().is_some_and(|defs| {
            defs.iter().any(|d| {
                d.get("name")
                    .or_else(|| d.pointer("/function/name"))
                    .and_then(Value::as_str)
                    == Some(&call.name)
                    && d.get("url")
                        .and_then(Value::as_str)
                        .is_some_and(|u| !u.is_empty())
            })
        })
    }
    pub fn definitions(&self) -> Value {
        let mut defs:Vec<Value>=self.cfg.value("tools").and_then(Value::as_array).map(|tools|tools.iter().map(|t|{
            if t.get("function").is_some(){t.clone()}else{json!({"type":"function","function":{"name":t["name"],"description":t["description"],"parameters":t.get("parameters").cloned().unwrap_or(json!({"type":"object","properties":{}}))}})}
        }).collect()).unwrap_or_default();
        if self.cfg.bool("tools_memory", false) {
            defs.push(json!({"type":"function","function":{"name":"update_memory","description":"Update confirmed facts for this call. Merge patch; null deletes a key.","parameters":{"type":"object","properties":{"patch":{"type":"object"}},"required":["patch"]}}}));
        }
        if self.cfg.bool("tools_end_call", false) {
            defs.push(json!({"type":"function","function":{"name":"end_call","description":"End the call only after the conversation and your farewell are finished.","parameters":{"type":"object","properties":{"reason":{"type":"string"}}}}}));
        }
        json!(defs)
    }
    pub async fn memory_block(&self) -> Option<String> {
        if !self.cfg.bool("tools_memory", false) {
            return None;
        }
        let m = self.memory.lock().await;
        Some(format!("Confirmed call memory version {}: {}", m.0, m.1))
    }
    pub async fn execute(
        &self,
        call: &ToolCall,
        epoch: Instant,
        turn_cancel: &CancellationToken,
    ) -> (Value, bool) {
        let arguments: Value = match serde_json::from_str(&call.arguments) {
            Ok(v) => v,
            Err(_) => return (json!({"error":"invalid JSON arguments"}), false),
        };
        if call.name == "update_memory" && self.cfg.bool("tools_memory", false) {
            let Some(patch) = arguments.get("patch").filter(|p| p.is_object()) else {
                return (json!({"error":"patch must be an object"}), false);
            };
            let mut memory = self.memory.lock().await;
            let mut updated = memory.1.clone();
            patch_memory(&mut updated, patch);
            if updated.to_string().len() > 65536 {
                return (json!({"error":"working memory exceeds 64 KiB"}), false);
            }
            memory.1 = updated;
            memory.0 += 1;
            return (
                json!({"ok":true,"version":memory.0,"memory":memory.1}),
                false,
            );
        }
        if call.name == "end_call" && self.cfg.bool("tools_end_call", false) {
            return if epoch.elapsed() < Duration::from_secs(10) {
                (
                    json!({"ok":false,"error":"call has only just started; continue the conversation"}),
                    false,
                )
            } else {
                (json!({"ok":true,"status":"ending_after_playback"}), true)
            };
        }
        let definition = self
            .server_tools
            .as_array()
            .and_then(|tools| {
                tools.iter().find(|t| {
                    t.get("name")
                        .or_else(|| t.pointer("/function/name"))
                        .and_then(Value::as_str)
                        == Some(&call.name)
                })
            })
            .cloned();
        let Some(definition) = definition else {
            return (
                json!({"error":"tool has no configured server executor"}),
                false,
            );
        };
        let memory = self.memory.lock().await.clone();
        let request = json!({"tool":call.name,"call_id":call.id,"arguments":arguments,"memory":memory.1,"memory_version":memory.0});
        if definition["async"].as_bool() == Some(true) {
            // Admission is bounded; do not spawn an unbounded queue of waiting tasks.
            let Ok(permit) = self.workers.clone().try_acquire_owned() else {
                return (
                    json!({"error":"tool worker capacity reached; retry later"}),
                    false,
                );
            };
            let runtime = self.clone();
            let name = call.name.clone();
            let id = call.id.clone();
            runtime.latest.lock().await.insert(name.clone(), id.clone());
            tokio::spawn(async move {
                let result = tokio::select! {_=runtime.cancel.cancelled()=>return,result=runtime.http(&definition,request)=>result};
                drop(permit);
                if runtime.latest.lock().await.get(&name) != Some(&id) {
                    return;
                }
                let output = result.unwrap_or_else(|_| json!({"error":"tool request failed"}));
                tokio::select! {_=runtime.cancel.cancelled()=>{},_=runtime.inner.send(Internal::ToolDelivery{name,id,version:memory.0,output})=>{}}
            });
            return (json!({"status":"running","task_id":call.id}), false);
        }
        let result = tokio::select! {_=turn_cancel.cancelled()=>return(json!({"error":"cancelled"}),false),result=self.http(&definition,request)=>result};
        (
            result.unwrap_or_else(|_| json!({"error":"tool request failed"})),
            false,
        )
    }
    async fn http(&self, definition: &Value, request: Value) -> Result<Value> {
        let path = definition["url"].as_str().unwrap_or("");
        if path.is_empty() {
            bail!("tool URL is not configured");
        }
        let url = if path.starts_with('/') {
            format!(
                "{}{}",
                self.cfg.string("tools_base_url", "").trim_end_matches('/'),
                path
            )
        } else {
            path.to_owned()
        };
        let timeout = definition["timeout_ms"]
            .as_u64()
            .unwrap_or(8000)
            .clamp(1, 120000);
        let client = reqwest::Client::builder()
            .timeout(Duration::from_millis(timeout))
            .build()?;
        let mut req = client.post(url).json(&request);
        let token_file = self.cfg.string("tools_token_file", "");
        if !token_file.is_empty() {
            let token = std::fs::read_to_string(token_file)?;
            req = req.bearer_auth(token.trim());
        }
        let mut response = req.send().await?.error_for_status()?;
        let mut body = Vec::new();
        while let Some(chunk) = response.chunk().await? {
            if body.len() + chunk.len() > 1 << 20 {
                bail!("tool result too large");
            }
            body.extend_from_slice(&chunk);
        }
        Ok(serde_json::from_slice(&body).unwrap_or_else(|_| json!(String::from_utf8_lossy(&body))))
    }
}
fn patch_memory(target: &mut Value, patch: &Value) {
    if !target.is_object() {
        *target = json!({});
    }
    for (key, value) in patch.as_object().expect("validated object") {
        if value.is_null() {
            target.as_object_mut().unwrap().remove(key);
        } else if value.is_object() {
            patch_memory(&mut target[key], value);
        } else {
            target[key] = value.clone();
        }
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn memory_null_deletes_recursively() {
        let mut state = json!({"quote":{"cover":true,"old":1},"name":"x"});
        patch_memory(
            &mut state,
            &json!({"quote":{"old":null,"new":2},"name":null}),
        );
        assert_eq!(state, json!({"quote":{"cover":true,"new":2}}));
    }
    #[tokio::test]
    async fn end_call_is_refused_during_first_ten_seconds() {
        let mut cfg = Config::default();
        cfg.set("tools_end_call", json!(true));
        let (tx, _) = mpsc::channel(1);
        let cancel = CancellationToken::new();
        let runtime = Runtime::new(cfg, tx, cancel.clone());
        let (result, end) = runtime
            .execute(
                &ToolCall {
                    id: "t".into(),
                    name: "end_call".into(),
                    arguments: "{}".into(),
                },
                Instant::now(),
                &cancel,
            )
            .await;
        assert!(!end);
        assert_eq!(result["ok"], false);
    }
    #[tokio::test]
    async fn oversized_memory_patch_preserves_previous_version() {
        let mut cfg = Config::default();
        cfg.set("tools_memory", json!(true));
        let (tx, _) = mpsc::channel(1);
        let cancel = CancellationToken::new();
        let runtime = Runtime::new(cfg, tx, cancel.clone());
        let call = ToolCall {
            id: "memory".into(),
            name: "update_memory".into(),
            arguments: json!({"patch":{"large":"x".repeat(65537)}}).to_string(),
        };
        let (result, _) = runtime.execute(&call, Instant::now(), &cancel).await;
        assert!(result.get("error").is_some());
        assert_eq!(runtime.memory.lock().await.0, 0);
    }
}
