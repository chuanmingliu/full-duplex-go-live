use super::*;
use anyhow::{Context, anyhow};
use futures_util::{SinkExt, StreamExt};
use serde_json::json;
use tokio::net::TcpStream;
use tokio_tungstenite::{
    MaybeTlsStream, WebSocketStream,
    tungstenite::{Message as WsMessage, client::IntoClientRequest},
};

type Socket = WebSocketStream<MaybeTlsStream<TcpStream>>;

pub(super) struct Session {
    cfg: Config,
    socket: Option<Socket>,
    last_used: tokio::time::Instant,
    stale_flush_acks: usize,
}

impl Session {
    pub(super) fn new(cfg: Config) -> Self {
        Self {
            cfg,
            socket: None,
            last_used: tokio::time::Instant::now(),
            stale_flush_acks: 0,
        }
    }

    pub(super) fn reset(&mut self) {
        // A cancelled task's unread audio must never be reused on the next turn.
        self.socket = None;
        self.stale_flush_acks = 0;
    }

    fn endpoint(&self) -> String {
        env(
            &self.cfg,
            "MINIMAX_TTS_WEBSOCKET_ENDPOINT",
            "wss://api.minimaxi.com/ws/v1/t2a_v2",
        )
    }

    fn task_start(&self) -> Result<Value> {
        let cfg = &self.cfg;
        let voice = voice(cfg);
        if voice.is_empty() {
            bail!("MiniMax requires MINIMAX_TTS_VOICE_ID or voice in the profile");
        }
        let mut setting = json!({"voice_id":voice,"speed":cfg.f64("speed",env_number(cfg,"MINIMAX_TTS_SPEED",1.2)),
            "vol":env_number(cfg,"MINIMAX_TTS_VOL",1.0),"pitch":cfg.secret("MINIMAX_TTS_PITCH").parse::<f64>().ok().filter(|n| n.is_finite()).unwrap_or(0.0),
            "english_normalization":cfg.secret("MINIMAX_TTS_ENGLISH_NORMALIZATION").parse::<bool>().unwrap_or(false)});
        let emotion = cfg.secret("MINIMAX_TTS_EMOTION");
        if !emotion.is_empty() {
            setting["emotion"] = json!(emotion);
        }
        for (key, name) in [
            ("voice_modify", "MINIMAX_TTS_VOICE_MODIFY"),
            ("timber_weights", "MINIMAX_TTS_TIMBER_WEIGHTS"),
        ] {
            let raw = cfg.secret(name);
            if !raw.is_empty() {
                setting[key] = serde_json::from_str(&raw)
                    .with_context(|| format!("invalid JSON in {name}"))?;
            }
        }
        let mut start = json!({"event":"task_start","model":env(cfg,"MINIMAX_TTS_MODEL","speech-2.8-turbo"),
            "language_boost":env(cfg,"MINIMAX_TTS_LANGUAGE_BOOST","auto"),"voice_setting":setting,
            "audio_setting":{"sample_rate":16000,"format":"pcm","channel":1},
            "continuous_sound":cfg.secret("MINIMAX_TTS_CONTINUOUS_SOUND").parse::<bool>().unwrap_or(false)});
        let pronunciation = cfg.secret("MINIMAX_TTS_PRONUNCIATION");
        if !pronunciation.is_empty() {
            let entries: Vec<&str> = pronunciation
                .split([',', '\n'])
                .map(str::trim)
                .filter(|s| !s.is_empty())
                .collect();
            start["pronunciation_dict"] = json!({"tone":entries});
        }
        Ok(start)
    }

    pub(super) async fn prewarm(&mut self) -> Result<()> {
        self.connect().await
    }

    pub(super) async fn keep_alive(&mut self) {
        let seconds = env_number(&self.cfg, "MINIMAX_TTS_KEEPALIVE_S", 30.0);
        if seconds <= 0.0
            || self.socket.is_none()
            || self.last_used.elapsed().as_secs_f64() < seconds
        {
            return;
        }
        let socket = self.socket.as_mut().unwrap();
        match tokio::time::timeout(
            Duration::from_secs(2),
            socket.send(WsMessage::Ping(Vec::new().into())),
        )
        .await
        {
            Ok(Ok(())) => self.last_used = tokio::time::Instant::now(),
            _ => self.reset(),
        }
    }

    async fn connect(&mut self) -> Result<()> {
        let max_idle = timeout_seconds(&self.cfg, "MINIMAX_TTS_WEBSOCKET_MAX_IDLE_S", 90.0);
        if self.socket.is_some() && self.last_used.elapsed() < max_idle {
            return Ok(());
        }
        self.reset();
        let key = required(&self.cfg, "MINIMAX_TTS_API_KEY")?;
        let mut request = self
            .endpoint()
            .into_client_request()
            .context("invalid MiniMax WebSocket endpoint")?;
        request.headers_mut().insert(
            "Authorization",
            format!("Bearer {key}")
                .parse()
                .context("invalid MiniMax API key header")?,
        );
        let timeout = timeout_seconds(&self.cfg, "MINIMAX_TTS_WEBSOCKET_OPEN_TIMEOUT_S", 5.0);
        let (socket, _) = tokio::time::timeout(timeout, tokio_tungstenite::connect_async(request))
            .await
            .context("MiniMax connection timeout")?
            .map_err(|_| anyhow!("MiniMax WebSocket connection failed"))?;
        self.socket = Some(socket);
        self.expect("connected_success", timeout).await?;
        self.send(self.task_start()?).await?;
        self.expect("task_started", timeout).await?;
        self.last_used = tokio::time::Instant::now();
        Ok(())
    }

    async fn send(&mut self, value: Value) -> Result<()> {
        let socket = self.socket.as_mut().context("MiniMax socket is closed")?;
        tokio::time::timeout(
            Duration::from_secs(10),
            socket.send(WsMessage::Text(value.to_string().into())),
        )
        .await
        .context("MiniMax write timeout")??;
        Ok(())
    }

    async fn receive(&mut self, timeout: Duration) -> Result<Value> {
        let deadline = tokio::time::Instant::now() + timeout;
        loop {
            let socket = self.socket.as_mut().context("MiniMax socket is closed")?;
            let message = tokio::time::timeout_at(deadline, socket.next())
                .await
                .context("MiniMax receive timeout")?
                .ok_or_else(|| anyhow!("MiniMax closed before completing synthesis"))??;
            let data = match message {
                WsMessage::Text(text) => text.as_bytes().to_vec(),
                WsMessage::Binary(data) => data.to_vec(),
                WsMessage::Ping(data) => {
                    tokio::time::timeout_at(deadline, socket.send(WsMessage::Pong(data)))
                        .await
                        .context("MiniMax pong timeout")??;
                    continue;
                }
                WsMessage::Close(_) => bail!("MiniMax closed before completing synthesis"),
                _ => continue,
            };
            let frame: Value =
                serde_json::from_slice(&data).context("MiniMax invalid JSON frame")?;
            if let Some(code) = frame["base_resp"]["status_code"]
                .as_i64()
                .filter(|c| *c != 0)
            {
                bail!("MiniMax synthesis failed with status {code}");
            }
            if frame["event"] == "task_failed" {
                bail!("MiniMax synthesis task failed");
            }
            return Ok(frame);
        }
    }

    async fn expect(&mut self, event: &str, timeout: Duration) -> Result<()> {
        let deadline = tokio::time::Instant::now() + timeout;
        loop {
            let frame = self
                .receive(deadline.saturating_duration_since(tokio::time::Instant::now()))
                .await?;
            if frame["event"] == event {
                return Ok(());
            }
        }
    }

    pub(super) async fn speak(
        &mut self,
        text: &str,
        out: &mpsc::Sender<Result<Vec<u8>>>,
    ) -> Result<()> {
        let text = text.trim();
        if text.is_empty() {
            return Ok(());
        }
        self.connect().await?;
        self.send(json!({"event":"task_continue","text":text}))
            .await?;
        let flush = self.endpoint().ends_with("_bidi")
            && !text.ends_with(['。', '！', '？', '!', '?', '.', '…', '\n'])
            && env(&self.cfg, "MINIMAX_TTS_FLUSH_PARTIAL_SEGMENTS", "true")
                .parse::<bool>()
                .unwrap_or(true);
        if flush {
            self.send(json!({"event":"task_flush"})).await?;
        }
        let receive_timeout =
            timeout_seconds(&self.cfg, "MINIMAX_TTS_WEBSOCKET_RECEIVE_TIMEOUT_S", 30.0);
        let mut incremental = false;
        loop {
            let frame = self.receive(receive_timeout).await?;
            if let Some(audio) = frame["data"]["audio"].as_str().filter(|s| !s.is_empty()) {
                let aggregate = frame["data"]["status"].as_i64() == Some(2);
                if !(aggregate && incremental) {
                    let pcm = decode_hex(audio)?;
                    if pcm.len() % 2 != 0 {
                        bail!("MiniMax returned incomplete PCM16 samples");
                    }
                    if !aggregate {
                        incremental = true;
                    }
                    tokio::time::timeout(receive_timeout, out.send(Ok(pcm)))
                        .await
                        .context("MiniMax audio consumer stalled")?
                        .map_err(|_| anyhow!("MiniMax audio consumer closed"))?;
                }
            }
            if frame["event"] == "task_flushed" {
                if self.stale_flush_acks > 0 {
                    self.stale_flush_acks -= 1;
                    continue;
                }
                if !flush {
                    continue;
                }
                self.last_used = tokio::time::Instant::now();
                return Ok(());
            }
            if frame["is_final"] == true {
                if flush {
                    self.stale_flush_acks += 1;
                }
                self.last_used = tokio::time::Instant::now();
                return Ok(());
            }
        }
    }
}

fn decode_hex(value: &str) -> Result<Vec<u8>> {
    if value.len() % 2 != 0 {
        bail!("MiniMax audio is not valid hexadecimal");
    }
    value
        .as_bytes()
        .chunks_exact(2)
        .map(|pair| {
            let high = (pair[0] as char)
                .to_digit(16)
                .context("MiniMax audio is not valid hexadecimal")?;
            let low = (pair[1] as char)
                .to_digit(16)
                .context("MiniMax audio is not valid hexadecimal")?;
            Ok(((high << 4) | low) as u8)
        })
        .collect()
}

pub(super) async fn synthesize(
    cfg: Config,
    text: String,
    cancel: CancellationToken,
) -> Result<mpsc::Receiver<Result<Vec<u8>>>> {
    let (out, rx) = mpsc::channel(16);
    tokio::spawn(async move {
        let mut session = Session::new(cfg);
        tokio::select! {
            _=cancel.cancelled()=>{}, _=out.closed()=>{},
            result=session.speak(&text,&out)=>if let Err(error)=result {
                tokio::select! { _=cancel.cancelled()=>{}, _=out.send(Err(error))=>{} }
            }
        }
    });
    Ok(rx)
}

#[cfg(test)]
mod tests {
    use super::super::tests::config;
    use super::*;

    #[test]
    fn invalid_audio_is_rejected() {
        assert_eq!(decode_hex("00Ff0102").unwrap(), vec![0, 255, 1, 2]);
        assert!(decode_hex("00x1").is_err());
        assert!(decode_hex("0").is_err());
    }

    #[tokio::test]
    async fn persistent_socket_skips_aggregate_audio_and_stale_flush_ack() {
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let endpoint = format!("ws://{}/t2a_v2_bidi", listener.local_addr().unwrap());
        let server = tokio::spawn(async move {
            let (stream, _) = listener.accept().await.unwrap();
            let mut ws = tokio_tungstenite::accept_async(stream).await.unwrap();
            ws.send(WsMessage::Text(
                json!({"event":"connected_success"}).to_string().into(),
            ))
            .await
            .unwrap();
            let start: Value =
                serde_json::from_str(ws.next().await.unwrap().unwrap().to_text().unwrap()).unwrap();
            assert_eq!(start["audio_setting"]["sample_rate"], 16000);
            ws.send(WsMessage::Text(
                json!({"event":"sentence_start"}).to_string().into(),
            ))
            .await
            .unwrap();
            ws.send(WsMessage::Text(
                json!({"event":"task_started"}).to_string().into(),
            ))
            .await
            .unwrap();
            assert!(
                ws.next()
                    .await
                    .unwrap()
                    .unwrap()
                    .to_text()
                    .unwrap()
                    .contains("task_continue")
            );
            assert!(
                ws.next()
                    .await
                    .unwrap()
                    .unwrap()
                    .to_text()
                    .unwrap()
                    .contains("task_flush")
            );
            for frame in [
                json!({"data":{"audio":"0100","status":1}}),
                json!({"data":{"audio":"0100","status":2},"is_final":true}),
                json!({"event":"task_flushed"}),
            ] {
                ws.send(WsMessage::Text(frame.to_string().into()))
                    .await
                    .unwrap();
            }
            assert!(
                ws.next()
                    .await
                    .unwrap()
                    .unwrap()
                    .to_text()
                    .unwrap()
                    .contains("task_continue")
            );
            ws.send(WsMessage::Text(
                json!({"data":{"audio":"0200","status":1},"is_final":true})
                    .to_string()
                    .into(),
            ))
            .await
            .unwrap();
        });
        let mut cfg = config(json!({"tts":"minimax"}));
        cfg.env.extend([
            ("MINIMAX_TTS_API_KEY".into(), "fake".into()),
            ("MINIMAX_TTS_VOICE_ID".into(), "test".into()),
            ("MINIMAX_TTS_WEBSOCKET_ENDPOINT".into(), endpoint),
        ]);
        let synth = Synthesizer::new(cfg);
        let mut first = synth
            .synthesize("你好".into(), CancellationToken::new())
            .await
            .unwrap();
        assert_eq!(first.recv().await.unwrap().unwrap(), vec![1, 0]);
        assert!(first.recv().await.is_none());
        let mut second = synth
            .synthesize("再见。".into(), CancellationToken::new())
            .await
            .unwrap();
        assert_eq!(second.recv().await.unwrap().unwrap(), vec![2, 0]);
        assert!(second.recv().await.is_none());
        server.await.unwrap();
    }
    #[tokio::test]
    async fn cancelled_segment_discards_socket_before_next_synthesis() {
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let endpoint = format!("ws://{}", listener.local_addr().unwrap());
        let (started_tx, started_rx) = tokio::sync::oneshot::channel();
        let server = tokio::spawn(async move {
            let mut started_tx = Some(started_tx);
            for generation in 0..2 {
                let (stream, _) = listener.accept().await.unwrap();
                let mut ws = tokio_tungstenite::accept_async(stream).await.unwrap();
                ws.send(WsMessage::Text(
                    json!({"event":"connected_success"}).to_string().into(),
                ))
                .await
                .unwrap();
                let _ = ws.next().await.unwrap().unwrap();
                ws.send(WsMessage::Text(
                    json!({"event":"task_started"}).to_string().into(),
                ))
                .await
                .unwrap();
                let _ = ws.next().await.unwrap().unwrap();
                if generation == 0 {
                    started_tx.take().unwrap().send(()).unwrap();
                    let ended = tokio::time::timeout(Duration::from_secs(1), ws.next())
                        .await
                        .unwrap();
                    assert!(ended.is_none() || ended.unwrap().is_err());
                } else {
                    ws.send(WsMessage::Text(
                        json!({"data":{"audio":"0300","status":1},"is_final":true})
                            .to_string()
                            .into(),
                    ))
                    .await
                    .unwrap();
                }
            }
        });
        let mut cfg = config(json!({"tts":"minimax"}));
        cfg.env.extend([
            ("MINIMAX_TTS_API_KEY".into(), "fake".into()),
            ("MINIMAX_TTS_VOICE_ID".into(), "test".into()),
            ("MINIMAX_TTS_WEBSOCKET_ENDPOINT".into(), endpoint),
        ]);
        let synth = Synthesizer::new(cfg);
        let cancel = CancellationToken::new();
        let mut first = synth
            .synthesize("old".into(), cancel.clone())
            .await
            .unwrap();
        started_rx.await.unwrap();
        cancel.cancel();
        assert!(
            tokio::time::timeout(Duration::from_secs(1), first.recv())
                .await
                .unwrap()
                .is_none()
        );
        let mut next = synth
            .synthesize("new".into(), CancellationToken::new())
            .await
            .unwrap();
        assert_eq!(next.recv().await.unwrap().unwrap(), vec![3, 0]);
        server.await.unwrap();
    }
}
