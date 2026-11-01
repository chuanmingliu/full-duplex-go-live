//! Fish Audio streams PCM over HTTP; dropping the body cancels an interrupted segment.
use super::*;
use anyhow::{Context, anyhow};
use futures_util::StreamExt;
use serde_json::json;

pub(super) async fn synthesize(
    cfg: Config,
    text: String,
    cancel: CancellationToken,
) -> Result<mpsc::Receiver<Result<Vec<u8>>>> {
    let (out, rx) = mpsc::channel(16);
    tokio::spawn(async move {
        let run = async {
            if text.trim().is_empty() {
                return Ok(());
            }
            let key = required(&cfg, "FISH_TTS_API_KEY")?;
            let mut voice = cfg.string("voice", "");
            if voice.is_empty() {
                voice = cfg.secret("FISH_TTS_VOICE_ID");
            }
            let speed = cfg.f64("speed", env_number(&cfg, "FISH_TTS_SPEED", 1.0));
            let mut body = json!({"text":text.trim(),"format":"pcm","sample_rate":16000,"latency":env(&cfg,"FISH_TTS_LATENCY","normal")});
            if !voice.is_empty() {
                body["reference_id"] = json!(voice);
            }
            if speed != 0.0 && speed != 1.0 {
                body["prosody"] = json!({"speed":speed});
            }
            let response = tokio::time::timeout(
                Duration::from_secs(30),
                super::deepseek::shared_client()?
                    .post(env(
                        &cfg,
                        "FISH_TTS_ENDPOINT",
                        "https://api.fish.audio/v1/tts",
                    ))
                    .bearer_auth(key)
                    .header("model", env(&cfg, "FISH_TTS_MODEL", "s2.1-pro"))
                    .json(&body)
                    .send(),
            )
            .await
            .context("Fish TTS response timeout")?
            .map_err(|_| anyhow!("Fish TTS HTTP request failed"))?;
            if !response.status().is_success() {
                bail!("Fish TTS returned HTTP {}", response.status());
            }
            let mut stream = response.bytes_stream();
            let mut carry = None;
            while let Some(chunk) = tokio::time::timeout(Duration::from_secs(30), stream.next())
                .await
                .context("Fish TTS stream stalled")?
            {
                let bytes = chunk.context("Fish TTS stream read failed")?;
                let mut pcm = Vec::with_capacity(bytes.len() + 1);
                if let Some(byte) = carry.take() {
                    pcm.push(byte);
                }
                pcm.extend_from_slice(&bytes);
                if pcm.len() % 2 != 0 {
                    carry = pcm.pop();
                }
                if !pcm.is_empty() && out.send(Ok(pcm)).await.is_err() {
                    return Ok(());
                }
            }
            if carry.is_some() {
                bail!("Fish TTS ended with an incomplete PCM16 sample");
            }
            Ok(())
        };
        tokio::select! {
            _=cancel.cancelled()=>{}, _=out.closed()=>{},
            result=run=>if let Err(error)=result {
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
    use tokio::io::{AsyncReadExt, AsyncWriteExt};

    #[tokio::test]
    async fn odd_http_chunks_preserve_complete_pcm_samples() {
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let endpoint = format!("http://{}", listener.local_addr().unwrap());
        let server = tokio::spawn(async move {
            let (mut socket, _) = listener.accept().await.unwrap();
            let mut request = vec![0; 8192];
            let _ = socket.read(&mut request).await.unwrap();
            socket.write_all(b"HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\nContent-Type: application/octet-stream\r\n\r\n1\r\n\x01\r\n2\r\n\x02\x03\r\n1\r\n\x04\r\n0\r\n\r\n").await.unwrap();
        });
        let mut cfg = config(json!({"tts":"fish"}));
        cfg.env.insert("FISH_TTS_API_KEY".into(), "fake".into());
        cfg.env.insert("FISH_TTS_ENDPOINT".into(), endpoint);
        let mut rx = synthesize(cfg, "test".into(), CancellationToken::new())
            .await
            .unwrap();
        let mut pcm = vec![];
        while let Some(chunk) = rx.recv().await {
            let chunk = chunk.unwrap();
            assert_eq!(chunk.len() % 2, 0);
            pcm.extend(chunk);
        }
        assert_eq!(pcm, vec![1, 2, 3, 4]);
        server.await.unwrap();
    }
}
