//! Profile loading without mutating the process environment or exposing secrets.
use anyhow::{Context, Result, bail};
use serde_json::{Value, json};
use std::{collections::HashMap, fmt, path::Path};

#[derive(Clone)]
pub struct Config {
    pub profile: Value,
    pub env: HashMap<String, String>,
}

impl fmt::Debug for Config {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Config")
            .field("profile", &self.redacted())
            .finish_non_exhaustive()
    }
}

impl Default for Config {
    fn default() -> Self {
        Self {
            env: HashMap::new(),
            profile: json!({
                "addr":"127.0.0.1:8766", "log_level":"info", "log_format":"text",
                "web_root":"", "auth_required":false, "allow_provider_override":false,
                "enable_metrics":true, "metrics_public":false, "allowed_origins":[],
                "max_sessions":64,"max_sessions_per_ip":8,"shutdown_timeout_seconds":10,
                "asr":"mock","llm":"mock","tts":"mock","model":"golive-1",
                "voice":"","language":"zh","speed":1.0,"client_rate":24000,
                "instructions":"You are a calm, friendly voice assistant. Keep answers to one or two short sentences. Never read out markup, lists or code.",
                "backend_model":"","backend_base_url":"",
                "temperature":0.7,"max_output_tokens":512,"disable_thinking":true,
                "history_turns":16,"history_max_chars":2000,"llm_hedge":1,
                "realtime":{"enabled":true,"sample_rate":24000,"dialect":"beta",
                    "hold_for_opening":false,"hold_timeout_seconds":45,
                    "honor_turn_detection":true,"min_silence_floor_ms":200},
                "duplex":{"allow_barge_in":true,"on_new_query":"cut","speculative":true,
                    "speculative_stable_ms":180,"speculative_min_chars":4,"early_final_ms":120,
                    "semantic_endpointing":true,"patient_silence_ms":700,
                    "ignore_hesitations":true,"user_backchannel_phrases":[],
                    "user_backchannel_hold_ms":600,"playback_paced":true,
                    "playback_chunk_ms":40,"stream_first_chunk_chars":6,
                    "stream_first_chunk_deadline_ms":220,"stream_min_chunk_chars":24,
                    "stream_max_chunk_chars":120,"phrase_cache":true,"phrase_cache_learn_chars":12,
                    "delegation_mode":"responses","client_delegation":"always",
                    "max_append_chars":1500,"session_max_seconds":3600,"idle_timeout_seconds":300},
                "vad":{"frame_ms":20,"min_speech_ms":200,"min_silence_ms":380,
                    "speech_pad_ms":240,"max_speech_ms":30000,"snapshot_ms":40,
                    "noise_floor_db":-60,"max_noise_floor_db":-28,"speech_margin_db":10,
                    "max_zcr":0.55,"gap_tolerance_ms":160,"barge_in_margin_db":6,
                    "barge_in_min_speech_ms":320,"line_fill_ms":100}
            }),
        }
    }
}

impl Config {
    pub fn load(profile: Option<&Path>, dotenv: Option<&Path>) -> Result<Self> {
        let mut cfg = Self::default();
        if let Some(path) = profile {
            let data = std::fs::read(path)
                .with_context(|| format!("reading profile {}", path.display()))?;
            let value: Value = serde_json::from_slice(&data).context("invalid profile JSON")?;
            if !value.is_object() {
                bail!("profile must be a JSON object");
            }
            merge(&mut cfg.profile, value);
        }
        if let Some(path) = dotenv {
            match dotenvy::from_path_iter(path) {
                Ok(vars) => {
                    for var in vars {
                        let (key, value) = var.context("invalid dotenv file")?;
                        cfg.env.insert(key, value);
                    }
                }
                Err(dotenvy::Error::Io(e)) if e.kind() == std::io::ErrorKind::NotFound => {}
                Err(e) => return Err(e).context("reading dotenv file"),
            }
        }
        cfg.env.extend(std::env::vars());
        cfg.apply_env()?;
        cfg.validate()?;
        Ok(cfg)
    }

    pub fn value(&self, path: &str) -> Option<&Value> {
        path.split('.').try_fold(&self.profile, |v, key| v.get(key))
    }
    pub fn string(&self, path: &str, default: &str) -> String {
        self.value(path)
            .and_then(Value::as_str)
            .unwrap_or(default)
            .to_owned()
    }
    pub fn bool(&self, path: &str, default: bool) -> bool {
        self.value(path).and_then(Value::as_bool).unwrap_or(default)
    }
    pub fn usize(&self, path: &str, default: usize) -> usize {
        self.value(path)
            .and_then(Value::as_u64)
            .and_then(|n| usize::try_from(n).ok())
            .unwrap_or(default)
    }
    pub fn f64(&self, path: &str, default: f64) -> f64 {
        self.value(path).and_then(Value::as_f64).unwrap_or(default)
    }
    pub fn secret(&self, name: &str) -> String {
        self.env.get(name).cloned().unwrap_or_default()
    }
    pub fn set(&mut self, path: &str, value: Value) {
        let keys: Vec<_> = path.split('.').collect();
        let mut current = &mut self.profile;
        for key in &keys[..keys.len() - 1] {
            if !current.is_object() {
                *current = json!({});
            }
            current = current
                .as_object_mut()
                .unwrap()
                .entry(*key)
                .or_insert_with(|| json!({}));
        }
        if !current.is_object() {
            *current = json!({});
        }
        current[keys[keys.len() - 1]] = value;
    }

    fn apply_env(&mut self) -> Result<()> {
        // Names match the Go service. Credentials stay in env and never enter a printed profile.
        let aliases = [
            ("REALTIME_ENABLED", "realtime.enabled"),
            ("REALTIME_RATE", "realtime.sample_rate"),
            ("REALTIME_DIALECT", "realtime.dialect"),
            ("REALTIME_HOLD", "realtime.hold_for_opening"),
            (
                "REALTIME_HONOR_TURN_DETECTION",
                "realtime.honor_turn_detection",
            ),
            ("DELEGATION_MODE", "duplex.delegation_mode"),
            ("CLIENT_DELEGATION", "duplex.client_delegation"),
            ("ALLOW_BARGE_IN", "duplex.allow_barge_in"),
            ("ON_NEW_QUERY", "duplex.on_new_query"),
            ("SPECULATIVE", "duplex.speculative"),
            ("PLAYBACK_PACED", "duplex.playback_paced"),
            ("IDLE_TIMEOUT_SECONDS", "duplex.idle_timeout_seconds"),
        ];
        let pairs: Vec<_> = self
            .env
            .iter()
            .filter_map(|(k, v)| {
                k.strip_prefix("GOLIVE_")
                    .map(|k| (k.to_owned(), v.to_owned()))
            })
            .collect();
        for (name, value) in pairs {
            if name.starts_with("MOCK_") || value.is_empty() {
                continue;
            }
            let lower = name.to_ascii_lowercase();
            let path = aliases
                .iter()
                .find(|(n, _)| *n == name)
                .map(|(_, p)| *p)
                .unwrap_or(&lower);
            let parsed = match self.value(path) {
                Some(Value::Bool(_)) => Value::Bool(match value.as_str() {
                    "1" | "true" | "TRUE" | "True" => true,
                    "0" | "false" | "FALSE" | "False" => false,
                    _ => bail!("GOLIVE_{name} must be a boolean"),
                }),
                Some(Value::Number(_)) => serde_json::from_str::<Value>(&value)
                    .ok()
                    .filter(Value::is_number)
                    .with_context(|| format!("GOLIVE_{name} must be a number"))?,
                Some(Value::Array(_)) => json!(
                    value
                        .split(',')
                        .map(str::trim)
                        .filter(|s| !s.is_empty())
                        .collect::<Vec<_>>()
                ),
                Some(_) => Value::String(value),
                None if matches!(
                    name.as_str(),
                    "AUTH_TOKEN"
                        | "METRICS_TOKEN"
                        | "GREETING"
                        | "TELEMETRY_URL"
                        | "STORE_DIR"
                        | "TLS_CERT_FILE"
                        | "TLS_KEY_FILE"
                ) =>
                {
                    Value::String(value)
                }
                None => continue,
            };
            self.set(path, parsed);
        }
        Ok(())
    }

    pub fn validate(&self) -> Result<()> {
        fn types(actual: &Value, defaults: &Value, prefix: &str) -> Result<()> {
            if let Some(fields) = defaults.as_object() {
                if !actual.is_object() {
                    bail!("{prefix} must be an object");
                }
                for (key, default) in fields {
                    if let Some(value) = actual.get(key) {
                        let path = if prefix.is_empty() {
                            key.clone()
                        } else {
                            format!("{prefix}.{key}")
                        };
                        types(value, default, &path)?;
                    }
                }
            } else {
                let valid = match defaults {
                    Value::Bool(_) => actual.is_boolean(),
                    Value::Number(_) => {
                        if prefix.ends_with("_db")
                            || matches!(prefix, "speed" | "temperature" | "vad.max_zcr")
                        {
                            actual.as_f64().is_some()
                        } else {
                            actual.as_u64().is_some()
                        }
                    }
                    Value::String(_) => actual.is_string(),
                    Value::Array(_) => actual.is_array(),
                    _ => true,
                };
                if !valid {
                    bail!("{prefix} has the wrong JSON type");
                }
            }
            Ok(())
        }
        types(&self.profile, &Self::default().profile, "")?;
        for key in [
            "auth_token",
            "metrics_token",
            "tls_cert_file",
            "tls_key_file",
        ] {
            if self.value(key).is_some_and(|v| !v.is_string()) {
                bail!("{key} must be a string");
            }
        }
        for key in ["tls_cert_file", "tls_key_file"] {
            if !self.string(key, "").is_empty() {
                bail!(
                    "Rust TLS listener is not implemented; terminate TLS at a reverse proxy and remove {key}"
                );
            }
        }
        if !["cut", "queue", "finish_sentence"]
            .contains(&self.string("duplex.on_new_query", "cut").as_str())
        {
            bail!("duplex.on_new_query must be cut, queue, or finish_sentence");
        }
        for key in ["client_rate", "realtime.sample_rate"] {
            if ![8000, 16000, 24000, 48000].contains(&self.usize(key, 24000)) {
                bail!("{key} must be 8000, 16000, 24000, or 48000");
            }
        }
        for (key, min, max) in [
            ("vad.frame_ms", 1, 100),
            ("vad.max_speech_ms", 100, 120000),
            ("duplex.playback_chunk_ms", 1, 1000),
            ("llm_hedge", 0, 16),
            ("max_output_tokens", 1, 32768),
        ] {
            let n = self.usize(key, 0);
            if n < min || n > max {
                bail!("{key} must be between {min} and {max}");
            }
        }
        if !(0.1..=4.0).contains(&self.f64("speed", 1.0)) {
            bail!("speed must be between 0.1 and 4.0");
        }
        if self.bool("auth_required", false) && self.string("auth_token", "").is_empty() {
            bail!("auth_required needs GOLIVE_AUTH_TOKEN");
        }
        if !["beta", "ga"].contains(&self.string("realtime.dialect", "beta").as_str()) {
            bail!("realtime.dialect must be beta or ga");
        }
        Ok(())
    }

    pub fn warn_compatibility(&self) {
        for (key, reason) in [
            (
                "duplex.backchannel",
                "automatic spoken acknowledgements are not ported",
            ),
            (
                "duplex.holding_filler",
                "automatic holding phrases are not ported",
            ),
            (
                "enable_pprof",
                "Go pprof is unavailable in the Rust service",
            ),
            ("realtime.store_calls", "durable call storage is not ported"),
        ] {
            if self.bool(key, false) {
                tracing::warn!(setting = key, reason, "unsupported Rust setting");
            }
        }
        if !self.string("store_dir", "").is_empty() {
            tracing::warn!(
                "store_dir is not implemented; store requests and attach/fork/content return explicit errors"
            );
        }
        if self.string("duplex.on_new_query", "cut") == "finish_sentence" {
            tracing::warn!("finish_sentence currently queues until the complete response ends");
        }
    }
    pub fn redacted(&self) -> Value {
        fn scrub(value: &mut Value) {
            match value {
                Value::Object(map) => {
                    for (key, v) in map {
                        if ["token", "secret", "password", "api_key"]
                            .iter()
                            .any(|s| key.to_ascii_lowercase().contains(s))
                            && v.is_string()
                        {
                            *v = json!("[redacted]");
                        } else {
                            scrub(v);
                        }
                    }
                }
                Value::Array(values) => {
                    for v in values {
                        scrub(v);
                    }
                }
                _ => {}
            }
        }
        let mut profile = self.profile.clone();
        scrub(&mut profile);
        profile
    }
}

pub fn merge(target: &mut Value, patch: Value) {
    if let (Some(dest), Some(src)) = (target.as_object_mut(), patch.as_object()) {
        for (key, value) in src {
            merge(dest.entry(key).or_insert(Value::Null), value.clone());
        }
    } else {
        *target = patch;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn profile_merge_preserves_nested_defaults() {
        let mut c = Config::default();
        merge(&mut c.profile, json!({"vad":{"min_silence_ms":500}}));
        assert_eq!(c.usize("vad.frame_ms", 0), 20);
        assert_eq!(c.usize("vad.min_silence_ms", 0), 500);
        c.validate().unwrap();
    }
    #[test]
    fn invalid_environment_is_not_silently_ignored() {
        let mut c = Config::default();
        c.env.insert("GOLIVE_ALLOW_BARGE_IN".into(), "maybe".into());
        assert!(c.apply_env().is_err());
    }
    #[test]
    fn debug_and_printing_never_show_secrets() {
        let mut c = Config::default();
        c.env
            .insert("DEEPSEEK_API_KEY".into(), "private-value".into());
        c.set("auth_token", json!("private-value"));
        assert!(!format!("{c:?}").contains("private-value"));
        assert!(!c.redacted().to_string().contains("private-value"));
    }
}
