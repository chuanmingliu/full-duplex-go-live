//! Wire negotiation and the Realtime adapter around canonical Live events.
use anyhow::{Result, bail};
use base64::{Engine as _, engine::general_purpose::STANDARD};
use serde_json::{Value, json};
use std::time::{Duration, Instant};

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct AudioFormat {
    pub mime: String,
    pub rate: u32,
}

impl AudioFormat {
    pub fn new(mime: &str, rate: u32) -> Result<Self> {
        let rate = match mime {
            "audio/pcm" => match rate {
                8000 | 16000 | 24000 | 48000 => rate,
                _ => bail!("PCM rate must be 8000, 16000, 24000 or 48000"),
            },
            "audio/pcmu" | "audio/pcma" if rate == 0 || rate == 8000 => 8000,
            "audio/pcmu" | "audio/pcma" => bail!("G.711 audio is always 8000 Hz"),
            _ => bail!("unsupported audio format; use audio/pcm, audio/pcmu or audio/pcma"),
        };
        Ok(Self {
            mime: mime.into(),
            rate,
        })
    }

    pub fn from_live(session: &Value, default_rate: u32) -> Result<Self> {
        let f = &session["audio"]["format"];
        let mime = f["type"].as_str().unwrap_or("audio/pcm");
        let rate = f["rate"].as_u64().unwrap_or(if mime == "audio/pcm" {
            default_rate as u64
        } else {
            8000
        });
        Self::new(mime, u32::try_from(rate)?)
    }

    pub fn from_realtime(session: &Value, default_rate: u32) -> Result<Self> {
        let format = match session["input_audio_format"].as_str().unwrap_or("pcm16") {
            "pcm16" => "audio/pcm",
            "g711_ulaw" => "audio/pcmu",
            "g711_alaw" => "audio/pcma",
            _ => bail!("unsupported input_audio_format"),
        };
        let nested = &session["audio"]["input"]["format"];
        let mime = nested["type"].as_str().unwrap_or(format);
        let rate = nested["rate"].as_u64().unwrap_or(if mime == "audio/pcm" {
            default_rate as u64
        } else {
            8000
        });
        let result = Self::new(mime, u32::try_from(rate)?)?;
        if let Some(output) = session["output_audio_format"].as_str() {
            let input = match result.mime.as_str() {
                "audio/pcmu" => "g711_ulaw",
                "audio/pcma" => "g711_alaw",
                _ => "pcm16",
            };
            if output != input {
                bail!("input and output audio formats must match");
            }
        }
        if let Some(output) = session.pointer("/audio/output/format") {
            let output_mime = output["type"].as_str().unwrap_or(&result.mime);
            let output_rate = output["rate"].as_u64().unwrap_or(result.rate as u64);
            if output_mime != result.mime || output_rate != result.rate as u64 {
                bail!("input and output audio formats must match");
            }
        }
        Ok(result)
    }

    pub fn json(&self) -> Value {
        json!({"type": self.mime, "rate": self.rate})
    }

    pub fn decode(&self, bytes: &[u8]) -> Result<Vec<u8>> {
        let codec = crate::audio::Codec::from_mime(&self.mime).map_err(anyhow::Error::msg)?;
        let pcm = codec.decode(bytes).map_err(anyhow::Error::msg)?;
        crate::audio::resample_pcm16(&pcm, self.rate, 16000).map_err(anyhow::Error::msg)
    }

    pub fn encode(&self, pcm: &[u8]) -> Result<Vec<u8>> {
        let samples =
            crate::audio::resample_pcm16(pcm, 16000, self.rate).map_err(anyhow::Error::msg)?;
        let codec = crate::audio::Codec::from_mime(&self.mime).map_err(anyhow::Error::msg)?;
        Ok(codec.encode(&samples))
    }

    pub fn encode_event(&self, event: &mut Value) -> Result<()> {
        if event["type"] == "session.output_audio.delta" {
            let pcm = STANDARD.decode(event["delta"].as_str().unwrap_or(""))?;
            event["delta"] = Value::String(STANDARD.encode(self.encode(&pcm)?));
        }
        Ok(())
    }
}

pub fn content_text(value: &Value) -> Option<String> {
    if let Some(s) = value.as_str() {
        return Some(s.into());
    }
    value.as_array().map(|parts| {
        parts
            .iter()
            .filter_map(|p| p["text"].as_str())
            .collect::<Vec<_>>()
            .join("")
    })
}

pub fn error(code: &str, message: impl ToString, client_event: &str) -> Value {
    let mut result = json!({"type":"error","error":{"type":"invalid_request_error","code":code,"message":message.to_string()}});
    if !client_event.is_empty() {
        result["client_event_id"] = client_event.into();
        result["error"]["client_event_id"] = client_event.into();
    }
    result
}

pub fn realtime_session(session: &Value) -> Value {
    let format = &session["audio"]["format"];
    let wire = match format["type"].as_str() {
        Some("audio/pcmu") => "g711_ulaw",
        Some("audio/pcma") => "g711_alaw",
        _ => "pcm16",
    };
    json!({"id":session["id"],"object":"realtime.session","type":"realtime","model":session["model"],
        "instructions":session["instructions"],"voice":session["audio"]["output"]["voice"],
        "modalities":["text","audio"],"input_audio_format":wire,"output_audio_format":wire,
        "turn_detection":session.get("turn_detection").cloned().unwrap_or(json!({"type":"server_vad"})),
        "audio":{"input":{"format":format},"output":{"format":format,"voice":session["audio"]["output"]["voice"]}}})
}

/// One translator per socket, serialized with writes so response.created
/// always precedes the response's first audio and text.
pub struct Realtime {
    ga: bool,
    response_sequence: u64,
    response_id: String,
    item: Option<String>,
    heard_item: String,
    heard: String,
    started: bool,
    speech_start: Value,
    last_started: Option<Instant>,
}

impl Realtime {
    pub fn new(ga: bool) -> Self {
        Self {
            ga,
            response_sequence: 0,
            response_id: String::new(),
            item: None,
            heard_item: String::new(),
            heard: String::new(),
            started: false,
            speech_start: json!(0),
            last_started: None,
        }
    }

    pub fn cancel_is_echo(&self) -> bool {
        self.last_started
            .is_some_and(|t| t.elapsed() < Duration::from_millis(750))
    }

    fn audio_type(&self, suffix: &str) -> String {
        format!(
            "response.{}.{}",
            if self.ga { "output_audio" } else { "audio" },
            suffix
        )
    }

    fn begin(&mut self, item: &str) -> Vec<Value> {
        if self.item.as_deref() == Some(item) {
            return vec![];
        }
        let mut events = if self.item.is_some() {
            self.finish("completed")
        } else {
            vec![]
        };
        self.response_sequence += 1;
        self.response_id = format!("resp_{}", self.response_sequence);
        self.item = Some(item.into());
        events.push(json!({"type":"response.created","response":{"id":self.response_id,"object":"realtime.response","status":"in_progress"}}));
        events
    }

    fn finish(&mut self, status: &str) -> Vec<Value> {
        let Some(item) = self.item.take() else {
            return vec![];
        };
        vec![
            json!({"type":self.audio_type("done"),"response_id":self.response_id,"item_id":item,"output_index":0,"content_index":0}),
            json!({"type":"response.done","response":{"id":self.response_id,"object":"realtime.response","status":status}}),
        ]
    }

    fn speech_started(&mut self) -> Value {
        self.started = true;
        self.last_started = Some(Instant::now());
        json!({"type":"input_audio_buffer.speech_started","audio_start_ms":self.speech_start,"item_id":self.heard_item})
    }

    pub fn translate(&mut self, event: Value) -> Vec<Value> {
        match event["type"].as_str().unwrap_or("") {
            "session.started" | "session.updated" => vec![
                json!({"type":"session.updated","session":realtime_session(&event["session"])}),
            ],
            "golive.speech.started" => {
                self.speech_start = event["start_ms"].clone();
                self.started = false;
                if self.item.is_some() {
                    vec![]
                } else {
                    vec![self.speech_started()]
                }
            }
            "golive.speech.stopped" => {
                if !self.started {
                    return vec![];
                }
                self.started = false;
                vec![
                    json!({"type":"input_audio_buffer.speech_stopped","audio_end_ms":event["end_ms"],"item_id":self.heard_item}),
                ]
            }
            "golive.output_audio.truncated" => {
                let mut events = self.finish("cancelled");
                events.push(self.speech_started());
                events
            }
            "session.input_transcript.delta" => {
                let id = event["item_id"].as_str().unwrap_or("");
                if id != self.heard_item {
                    self.heard_item = id.into();
                    self.heard.clear();
                }
                let delta = event["delta"].as_str().unwrap_or("");
                let mut events = vec![];
                if !delta.is_empty() {
                    self.heard.push_str(delta);
                    events.push(json!({"type":"conversation.item.input_audio_transcription.delta","item_id":id,"content_index":0,"delta":delta}));
                }
                if event["final"].as_bool().unwrap_or(false) {
                    events.push(json!({"type":"conversation.item.input_audio_transcription.completed","item_id":id,"content_index":0,"transcript":self.heard}));
                }
                events
            }
            "session.output_audio.delta" | "session.output_transcript.delta" => {
                let item = event["item_id"].as_str().unwrap_or("");
                let mut events = self.begin(item);
                let name = if event["type"] == "session.output_audio.delta" {
                    self.audio_type("delta")
                } else {
                    format!(
                        "response.{}_transcript.delta",
                        if self.ga { "output_audio" } else { "audio" }
                    )
                };
                events.push(json!({"type":name,"response_id":self.response_id,"item_id":item,"output_index":0,"content_index":0,"delta":event["delta"]}));
                events
            }
            "golive.turn.metrics" if self.item.as_deref() == event["turn_id"].as_str() => {
                self.finish("completed")
            }
            "response.event" => {
                let inner = &event["event"];
                if inner["type"] != "response.output_item.done"
                    || inner["item"]["type"] != "function_call"
                {
                    return vec![];
                }
                vec![
                    json!({"type":"response.function_call_arguments.done","response_id":self.response_id,
                    "item_id":inner["item"]["id"],"output_index":inner["output_index"],"call_id":inner["item"]["call_id"],
                    "name":inner["item"]["name"],"arguments":inner["item"]["arguments"]}),
                ]
            }
            "error" => {
                let mut body = event["error"].clone();
                if let Some(id) = body.get("client_event_id").cloned() {
                    body["event_id"] = id;
                }
                vec![json!({"type":"error","error":body})]
            }
            _ => vec![],
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn realtime_floor_and_response_order() {
        let mut rt = Realtime::new(false);
        let events = rt.translate(
            json!({"type":"session.output_audio.delta","item_id":"answer","delta":"AA=="}),
        );
        assert_eq!(events[0]["type"], "response.created");
        assert_eq!(events[1]["type"], "response.audio.delta");
        assert!(
            rt.translate(json!({"type":"golive.speech.started","start_ms":10}))
                .is_empty()
        );
        let events =
            rt.translate(json!({"type":"golive.output_audio.truncated","item_id":"answer"}));
        assert_eq!(events[1]["response"]["status"], "cancelled");
        assert_eq!(events[2]["type"], "input_audio_buffer.speech_started");
        assert!(rt.cancel_is_echo());
    }

    #[test]
    fn audio_round_trip_telephony_and_pcm() {
        for mime in ["audio/pcmu", "audio/pcma"] {
            let f = AudioFormat::new(mime, 8000).unwrap();
            let pcm: Vec<_> = vec![6000_i16; 320]
                .iter()
                .flat_map(|s| s.to_le_bytes())
                .collect();
            let wire = f.encode(&pcm).unwrap();
            assert_eq!(wire.len(), 160);
            let decoded = f.decode(&wire).unwrap();
            assert_eq!(decoded.len(), pcm.len());
            assert!((i16::from_le_bytes([decoded[0], decoded[1]]) - 6000).abs() < 256);
        }
        assert!(AudioFormat::new("audio/pcmu", 16000).is_err());
        assert!(
            AudioFormat::new("audio/pcm", 16000)
                .unwrap()
                .decode(&[0])
                .is_err()
        );
    }
}
