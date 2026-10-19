use super::{Internal, Permit, message, tools};
use crate::{
    config::Config,
    provider::{self, LlmDelta, Message, ToolCall},
    segment::{SegmentConfig, Segmenter},
};
use anyhow::{Result, anyhow, bail};
use base64::{Engine as _, engine::general_purpose::STANDARD};
use serde_json::{Value, json};
use std::{collections::HashMap, sync::Arc, time::Duration};
use tokio::{
    sync::{Mutex, mpsc, watch},
    task::{JoinHandle, JoinSet},
    time::Instant,
};
use tokio_util::sync::CancellationToken;

// Session-local cache: the voice/config cannot collide with another caller.
type Cache = Arc<Mutex<HashMap<String, Vec<u8>>>>;

pub(super) struct Response {
    pub id: u64,
    pub item_id: String,
    pub cfg: Config,
    pub messages: Vec<Message>,
    pub speak: Option<String>,
    pub tts: provider::Synthesizer,
    pub tools: tools::Runtime,
    pub focus: crate::focus::Focus,
    pub cancel: CancellationToken,
    pub permit: watch::Receiver<Permit>,
    pub inner: mpsc::Sender<Internal>,
    pub epoch: Instant,
}

impl Response {
    pub async fn run(mut self) {
        let _lifetime = self.cancel.clone().drop_guard();
        let started = self.ms();
        let mut stats = Stats::default();
        let cancel = self.cancel.clone();
        let result = tokio::select! {
            _=cancel.cancelled()=>return,
            result=self.respond(&mut stats)=>result,
        };
        if self.cancel.is_cancelled() {
            return;
        }
        if result.is_err() {
            // Vendor errors may contain signed URLs or response text. Only stable
            // error categories reach the client and logs.
            self.event(json!({"type":"error","error":{"type":"server_error","code":"response_failed","message":"voice response failed; check the selected provider configuration and connectivity"}})).await;
        }
        let permit = *self.permit.borrow();
        let origin = permit.speech_end_ms;
        if permit.confirmed {
            self.event(json!({"type":"golive.turn.metrics","turn_id":self.item_id,"revision":1,
                "speech_end_ms":origin,"end_silence_ms":permit.silence_ms,"turn_start_ms":started-origin,
                "speculative":started<origin,"llm_first_token_ms":stats.first_token.map(|t|t-origin),
                "first_segment_ms":stats.first_segment.map(|t|t-origin),"tts_first_audio_ms":stats.first_tts.map(|t|t-origin),
                "first_audio_out_ms":stats.first_audio.map(|t|t-origin),"turn_complete_ms":self.ms()-origin,
                "output_audio_ms":stats.audio_samples as f64/16.0})).await;
        }
        let _ = self
            .inner
            .send(Internal::Completed {
                generation: self.id,
            })
            .await;
    }
    fn ms(&self) -> i64 {
        self.epoch.elapsed().as_millis() as i64
    }
    async fn event(&self, event: Value) {
        tokio::select! {_=self.cancel.cancelled()=>{},_=self.inner.send(Internal::Output{generation:self.id,event})=>{}}
    }
    async fn confirm(&mut self) -> Result<()> {
        loop {
            if self.permit.borrow().confirmed {
                return Ok(());
            }
            tokio::select! {_=self.cancel.cancelled()=>bail!("response cancelled"),result=self.permit.changed()=>{result.map_err(|_|anyhow!("confirmation closed"))?;}}
        }
    }
    async fn respond(&mut self, stats: &mut Stats) -> Result<()> {
        let cache: Cache = self.tools.audio_cache.clone();
        if let Some(text) = self.speak.take() {
            self.speak_segment(&text, stats, &cache).await?;
            return Ok(());
        }
        self.cfg.set("tools", self.tools.definitions());
        if let Some(memory) = self.tools.memory_block().await {
            self.messages.insert(1, message("system", memory));
        }
        for _ in 0..8 {
            let (first, stream) =
                hedged_llm(self.cfg.clone(), self.messages.clone(), self.cancel.clone()).await?;
            stats.first_token.get_or_insert(self.ms());
            if !first.text.is_empty() || first.tool_call.is_some() {
                self.event(json!({"type":"response.event","delegation_id":self.item_id,
                    "event":{"type":"response.output_text.delta","response_id":self.item_id,"delta":first.text}})).await;
            }
            let (segments, mut spoken) = mpsc::channel(8);
            let cfg = self.cfg.clone();
            let cancel = self.cancel.child_token();
            let producer_cancel = cancel.clone();
            let mut producer = AbortOnDrop(tokio::spawn(async move {
                segment_stream(cfg, first, stream, segments, producer_cancel).await
            }));
            let mut full_text = String::new();
            while let Some(text) = tokio::select! {_=self.cancel.cancelled()=>{cancel.cancel();producer.0.abort();return Ok(());},text=spoken.recv()=>text}
            {
                full_text.push_str(&text);
                if let Err(error) = self.speak_segment(&text, stats, &cache).await {
                    cancel.cancel();
                    producer.0.abort();
                    return Err(error);
                }
            }
            let (calls, usage) = (&mut producer.0).await??;
            if let Some(usage) = usage {
                self.event(json!({"type":"session.usage.updated","usage":usage}))
                    .await;
            }
            if calls.is_empty() {
                return Ok(());
            }
            self.confirm().await?; // No tool side effects from speculative completions.
            let history_start = self.messages.len();
            self.messages.push(Message {
                role: "assistant".into(),
                content: full_text,
                tool_call_id: None,
                tool_calls: calls.clone(),
            });
            let mut hangup = false;
            let mut external = Vec::new();
            for call in calls {
                if self.tools.is_external(&call) {
                    external.push(call);
                    continue;
                }
                let (output, end_call) = self.tools.execute(&call, self.epoch, &self.cancel).await;
                hangup |= end_call;
                self.messages.push(Message {
                    role: "tool".into(),
                    content: output.to_string(),
                    tool_call_id: Some(call.id),
                    tool_calls: vec![],
                });
            }
            tokio::select! {_=self.cancel.cancelled()=>return Ok(()),_=self.inner.send(Internal::ToolHistory{generation:self.id,messages:self.messages[history_start..].to_vec()})=>{}}
            if !external.is_empty() {
                for (index, call) in external.into_iter().enumerate() {
                    self.event(json!({"type":"response.event","delegation_id":self.item_id,
                        "event":{"type":"response.output_item.done","response_id":self.item_id,"output_index":index,
                        "item":{"type":"function_call","id":call.id,"call_id":call.id,"name":call.name,"arguments":call.arguments}}})).await;
                }
                return Ok(());
            }
            if hangup {
                tokio::select! {_=self.cancel.cancelled()=>return Ok(()),_=tokio::time::sleep(Duration::from_millis(700))=>{}}
                self.event(json!({"type":"golive.hangup","reason":"assistant_hangup"}))
                    .await;
                return Ok(());
            }
        }
        bail!("too many tool rounds")
    }
    async fn speak_segment(&mut self, text: &str, stats: &mut Stats, cache: &Cache) -> Result<()> {
        if text.trim().is_empty() {
            return Ok(());
        }
        stats.first_segment.get_or_insert(self.ms());
        let cached = if self.cfg.bool("duplex.phrase_cache", true) {
            cache.lock().await.get(text).cloned()
        } else {
            None
        };
        let mut audio = if let Some(pcm) = cached {
            let (tx, rx) = mpsc::channel(1);
            let _ = tx.send(Ok(pcm)).await;
            rx
        } else {
            self.tts
                .synthesize_with_urgency(
                    text.to_owned(),
                    self.cancel.child_token(),
                    stats.first_audio.is_none(),
                )
                .await?
        };
        let mut pending = std::collections::VecDeque::new();
        let mut collected = Vec::new();
        let (mut received, mut played, mut eof) = (0usize, 0usize, false);
        let mut progress = PlaybackText::new(text, self.cfg.f64("speed", 1.0));
        let mut progress_at = None;
        let chunk = self
            .cfg
            .usize("duplex.playback_chunk_ms", 40)
            .clamp(1, 1000)
            * 32;
        // Only a bounded lookahead is retained; receiving the next packet never
        // delays first audio while waiting for the entire synthesized sentence.
        const MAX_BUFFER: usize = 1_920_000;
        loop {
            while !eof && pending.len() < MAX_BUFFER {
                match audio.try_recv() {
                    Ok(pcm) => {
                        let pcm = pcm?;
                        if !pcm.is_empty() {
                            stats.first_tts.get_or_insert(self.ms());
                        }
                        append_audio(pcm, &mut pending, &mut collected, &mut received)?;
                    }
                    Err(mpsc::error::TryRecvError::Empty) => break,
                    Err(mpsc::error::TryRecvError::Disconnected) => eof = true,
                }
            }
            if pending.len() < chunk && !eof {
                match tokio::select! {_=self.cancel.cancelled()=>bail!("playback cancelled"),pcm=audio.recv()=>pcm}
                {
                    Some(pcm) => {
                        let pcm = pcm?;
                        if !pcm.is_empty() {
                            stats.first_tts.get_or_insert(self.ms());
                        }
                        append_audio(pcm, &mut pending, &mut collected, &mut received)?;
                    }
                    None => eof = true,
                }
                continue;
            }
            if pending.is_empty() {
                break;
            }
            self.confirm().await?;
            // Attribution may arrive while a provider/cache packet was queued.
            // Once audio has begun, a late verdict is observational, as in Go.
            if stats.first_audio.is_none() && self.focus.acts() {
                let span = *self.permit.borrow();
                if span.input_end_ms > span.input_start_ms {
                    let attribution = self.focus.attribute(span.input_start_ms, span.input_end_ms);
                    if attribution.veto {
                        self.event(json!({"type":"golive.focus","veto":true,"coverage":attribution.coverage})).await;
                        self.cancel.cancel();
                        bail!("focus veto");
                    }
                }
            }
            let length = chunk.min(pending.len());
            let packet: Vec<u8> = pending.drain(..length).collect();
            let start = self.ms();
            progress_at.get_or_insert(start);
            self.play_packet(packet, stats).await?;
            played += length / 2;
            let delta = progress.advance(played, received, eof && pending.is_empty(), eof);
            if !delta.is_empty() {
                let end = self.ms();
                self.spoken(delta, progress_at.unwrap_or(start), end)
                    .await?;
                progress_at = Some(end);
            }
        }
        if played == 0 {
            bail!("synthesizer returned no audio");
        }
        // EOF can arrive after the final full-sized packet. Finish its text only
        // now that its actual audio duration is known and playback has drained.
        let tail = progress.advance(played, received, true, true);
        if !tail.is_empty() {
            self.spoken(tail, progress_at.unwrap_or_else(|| self.ms()), self.ms())
                .await?;
        }
        let learn = self.cfg.usize("duplex.phrase_cache_learn_chars", 12);
        if self.cfg.bool("duplex.phrase_cache", true)
            && text.chars().count() <= learn
            && collected.len() == played * 2
        {
            let mut cache = cache.lock().await;
            if cache.len() >= 128 {
                cache.clear();
            }
            cache.insert(text.to_owned(), collected);
        }
        Ok(())
    }
    async fn spoken(&self, text: String, start_ms: i64, end_ms: i64) -> Result<()> {
        tokio::select! {
            _=self.cancel.cancelled()=>bail!("playback cancelled"),
            result=self.inner.send(Internal::Spoken{generation:self.id,text,start_ms,end_ms})=>result.map_err(|_|anyhow!("session ended")),
        }
    }
    async fn play_packet(&self, pcm: Vec<u8>, stats: &mut Stats) -> Result<()> {
        if self.cancel.is_cancelled() {
            bail!("playback cancelled");
        }
        let start = self.ms();
        let samples = pcm.len() / 2;
        let duration = Duration::from_secs_f64(samples as f64 / 16000.0);
        self.event(json!({"type":"session.output_audio.delta","item_id":self.item_id,"delta":STANDARD.encode(&pcm),"start_ms":start,"end_ms":start+duration.as_millis() as i64})).await;
        if self.cancel.is_cancelled() {
            bail!("playback cancelled");
        }
        stats.first_audio.get_or_insert(start);
        stats.audio_samples += samples;
        if self.cfg.bool("duplex.playback_paced", true) {
            tokio::select! {_=self.cancel.cancelled()=>bail!("playback cancelled"),_=tokio::time::sleep(duration)=>{}}
        }
        Ok(())
    }
}
#[derive(Default)]
struct Stats {
    first_token: Option<i64>,
    first_segment: Option<i64>,
    first_tts: Option<i64>,
    first_audio: Option<i64>,
    audio_samples: usize,
}

async fn hedged_llm(
    cfg: Config,
    messages: Vec<Message>,
    cancel: CancellationToken,
) -> Result<(LlmDelta, mpsc::Receiver<Result<LlmDelta>>)> {
    let mut jobs = JoinSet::new();
    let mut tokens = Vec::new();
    for lane in 0..cfg.usize("llm_hedge", 1).clamp(1, 16) {
        let child = cancel.child_token();
        tokens.push(child.clone());
        let mut cfg = cfg.clone();
        let messages = messages.clone();
        let key = match cfg.string("llm", "mock").as_str() {
            "deepseek" => Some("DEEPSEEK_API_KEY"),
            "openai-compatible" => Some("OPENAI_API_KEY"),
            "cerebras" => Some("CEREBRAS_API_KEY"),
            "inception" => Some("INCEPTION_API_KEY"),
            _ => None,
        };
        if let Some(key) = key {
            let keys = provider::api_keys(&cfg, key)?;
            cfg.env.insert(key.into(), keys[lane % keys.len()].clone());
        }
        cfg.set("_llm_lane", json!(lane));
        jobs.spawn(async move {
            let delay=cfg.usize("llm_hedge_after_ms",0)*lane;
            tokio::select!{_=child.cancelled()=>bail!("cancelled"),_=tokio::time::sleep(Duration::from_millis(delay as u64))=>{}}
            let mut stream=provider::stream_llm(cfg,messages,child.clone()).await?;
            loop {tokio::select! {
                _=child.cancelled()=>bail!("cancelled"),
                delta=stream.recv()=>match delta {
                    Some(Ok(delta)) if !delta.text.is_empty()||delta.tool_call.is_some()=>return Ok((lane,delta,stream)),
                    Some(Ok(_))=>{},Some(Err(e))=>return Err(e),None=>bail!("empty model response"),
                }
            }}
        });
    }
    while let Some(result) = tokio::select! {_=cancel.cancelled()=>{for t in &tokens{t.cancel();}bail!("cancelled");},result=jobs.join_next()=>result}
    {
        if let Ok(Ok((winner, first, stream))) = result {
            for (i, t) in tokens.iter().enumerate() {
                if i != winner {
                    t.cancel();
                }
            }
            jobs.abort_all();
            return Ok((first, stream));
        }
    }
    for t in tokens {
        t.cancel();
    }
    bail!("all model requests failed")
}

async fn segment_stream(
    cfg: Config,
    first: LlmDelta,
    mut stream: mpsc::Receiver<Result<LlmDelta>>,
    out: mpsc::Sender<String>,
    cancel: CancellationToken,
) -> Result<(Vec<ToolCall>, Option<Value>)> {
    let mut segmenter = Segmenter::new(SegmentConfig {
        first_chunk_chars: cfg.usize("duplex.stream_first_chunk_chars", 6),
        min_chunk_chars: cfg.usize("duplex.stream_min_chunk_chars", 24),
        max_chunk_chars: cfg.usize("duplex.stream_max_chunk_chars", 120),
    });
    let mut calls = Vec::new();
    let mut usage = None;
    let mut first = Some(first);
    let mut sent = false;
    let started = Instant::now();
    let deadline =
        Duration::from_millis(cfg.usize("duplex.stream_first_chunk_deadline_ms", 220) as u64);
    let mut tick = tokio::time::interval(Duration::from_millis(20));
    tick.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);
    loop {
        let delta = if let Some(first) = first.take() {
            Some(Ok(first))
        } else {
            tokio::select! {
                _=cancel.cancelled()=>return Ok((calls,usage)),
                delta=stream.recv()=>delta,
                _=tick.tick(), if !sent=>{
                    if started.elapsed()>=deadline&&segmenter.pending().chars().count()>=cfg.usize("duplex.stream_first_chunk_chars",6) {
                        let text=if segmenter.has_early_boundary(){segmenter.flush_first_at_boundary()}else{segmenter.flush_first()};
                        if !text.is_empty(){send_segment(&out,text,&cancel).await?;sent=true;}
                    }
                    continue;
                }
            }
        };
        let Some(delta) = delta else {
            break;
        };
        let delta = delta?;
        if let Some(call) = delta.tool_call {
            calls.push(call);
        }
        if delta.usage.is_some() {
            usage = delta.usage;
        }
        for text in segmenter.push(&delta.text) {
            send_segment(&out, text, &cancel).await?;
            sent = true;
        }
    }
    let remaining = segmenter.flush();
    if !remaining.is_empty() {
        send_segment(&out, remaining, &cancel).await?;
    }
    Ok((calls, usage))
}

// Dropping an outer response future aborts its segment producer immediately,
// including when that producer is blocked behind the bounded playback queue.
struct AbortOnDrop<T>(JoinHandle<T>);
impl<T> Drop for AbortOnDrop<T> {
    fn drop(&mut self) {
        self.0.abort();
    }
}
async fn send_segment(
    out: &mpsc::Sender<String>,
    text: String,
    cancel: &CancellationToken,
) -> Result<()> {
    tokio::select! {_=cancel.cancelled()=>bail!("segmentation cancelled"),result=out.send(text)=>result.map_err(|_|anyhow!("playback stopped"))}
}
fn append_audio(
    pcm: Vec<u8>,
    pending: &mut std::collections::VecDeque<u8>,
    cache: &mut Vec<u8>,
    received: &mut usize,
) -> Result<()> {
    if pcm.len() % 2 != 0 {
        bail!("synthesizer returned odd PCM");
    }
    if pending.len() + pcm.len() > 1_920_000 {
        bail!("synthesizer audio exceeds bounded playback buffer");
    }
    *received += pcm.len() / 2;
    if cache.len() + pcm.len() <= 256000 {
        cache.extend_from_slice(&pcm);
    }
    pending.extend(pcm);
    Ok(())
}

/// TTS has no word alignment. Keep a conservative, monotonic text-duration
/// estimate while it streams, then use the known sample duration at EOF.
/// The final character is withheld until all audio drains. This is an estimate
/// of heard text, not forced alignment, matching the Go player's limitation.
struct PlaybackText {
    characters: Vec<char>,
    emitted: usize,
    estimated_samples: usize,
}
impl PlaybackText {
    fn new(text: &str, speed: f64) -> Self {
        let characters: Vec<_> = text.chars().collect();
        let ms: usize = characters
            .iter()
            .map(|c| {
                if c.is_whitespace() {
                    20
                } else if c.is_ascii() {
                    80
                } else {
                    200
                }
            })
            .sum();
        let speed = if speed.is_finite() {
            speed.clamp(0.1, 4.0)
        } else {
            1.0
        };
        Self {
            characters,
            emitted: 0,
            estimated_samples: (ms as f64 / speed * 16.0) as usize,
        }
    }
    fn advance(
        &mut self,
        played: usize,
        received: usize,
        drained: bool,
        total_known: bool,
    ) -> String {
        let total = if total_known {
            received
        } else {
            received.max(self.estimated_samples)
        };
        let mut count = if drained {
            self.characters.len()
        } else {
            self.characters
                .len()
                .saturating_mul(played)
                .checked_div(total)
                .unwrap_or(0)
        };
        if !drained {
            count = count.min(self.characters.len().saturating_sub(1));
        }
        count = count.max(self.emitted).min(self.characters.len());
        let delta = self.characters[self.emitted..count].iter().collect();
        self.emitted = count;
        delta
    }
}
