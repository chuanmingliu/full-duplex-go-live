use super::*;
use anyhow::{Context, anyhow};
use base64::{Engine, engine::general_purpose::STANDARD};
use futures_util::{SinkExt, StreamExt};
use hmac::{Hmac, Mac};
use serde::Deserialize;
use sha1::Sha1;
use std::{
    collections::BTreeMap,
    time::{SystemTime, UNIX_EPOCH},
};
use tokio_tungstenite::tungstenite::Message as WsMessage;

fn signed_url(cfg: &Config, voice_id: &str, timestamp: u64) -> Result<String> {
    let app_id = required(cfg, "TENCENT_ASR_APP_ID")?;
    let secret_id = required(cfg, "TENCENT_ASR_SECRET_ID")?;
    let key = required(cfg, "TENCENT_ASR_SECRET_KEY")?;
    let mut params = BTreeMap::from([
        ("convert_num_mode", "0".into()),
        (
            "engine_model_type",
            env(cfg, "TENCENT_ASR_ENGINE", "16k_zh"),
        ),
        ("expired", (timestamp + 86400).to_string()),
        ("filter_dirty", "0".into()),
        ("filter_modal", "0".into()),
        ("filter_punc", "0".into()),
        ("needvad", "0".into()),
        ("nonce", timestamp.to_string()),
        ("secretid", secret_id),
        ("sub_service_type", "1".into()),
        ("timestamp", timestamp.to_string()),
        ("voice_format", "1".into()),
        ("voice_id", voice_id.into()),
        ("word_info", "0".into()),
    ]);
    let source = format!(
        "asr.cloud.tencent.com/asr/v2/{app_id}?{}",
        params
            .iter()
            .map(|(k, v)| format!("{k}={v}"))
            .collect::<Vec<_>>()
            .join("&")
    );
    let mut mac = Hmac::<Sha1>::new_from_slice(key.as_bytes()).context("Tencent signing key")?;
    mac.update(source.as_bytes());
    params.insert("signature", STANDARD.encode(mac.finalize().into_bytes()));
    let query = url::form_urlencoded::Serializer::new(String::new())
        .extend_pairs(params)
        .finish();
    Ok(format!(
        "wss://asr.cloud.tencent.com/asr/v2/{app_id}?{query}"
    ))
}

// Tencent's wire field is named `final`, which Rust reserves.
#[derive(Default, Deserialize)]
#[serde(default)]
struct WireFrame {
    code: i64,
    message: String,
    #[serde(rename = "final")]
    final_: i64,
    result: Option<Recognition>,
}

#[derive(Default, Deserialize)]
#[serde(default)]
struct Recognition {
    slice_type: i64,
    voice_text_str: String,
}

pub(super) async fn open_asr(cfg: Config, cancel: CancellationToken) -> Result<AsrSession> {
    let timestamp = SystemTime::now().duration_since(UNIX_EPOCH)?.as_secs();
    let endpoint = signed_url(
        &cfg,
        &format!("golive-rs-{}", uuid::Uuid::new_v4()),
        timestamp,
    )?;
    let endpoint = env(&cfg, "TENCENT_ASR_WEBSOCKET_ENDPOINT", &endpoint);
    let open_timeout = timeout_seconds(&cfg, "TENCENT_ASR_OPEN_TIMEOUT_S", 1.5);
    let (socket, _) = tokio::select! {
        _ = cancel.cancelled() => bail!("Tencent recognition cancelled"),
        result = tokio::time::timeout(open_timeout, tokio_tungstenite::connect_async(endpoint)) => {
            result.context("Tencent ASR connection timeout")?.map_err(|_| anyhow!("Tencent ASR WebSocket connection failed"))?
        }
    };
    let (commands, mut input) = mpsc::channel(64);
    let (out, results) = mpsc::channel(32);
    tokio::spawn(async move {
        let run = async {
            let (mut writer, mut reader) = socket.split();
            let mut stable = String::new();
            let mut partial = String::new();
            let mut send_done = false;
            let mut commands_closed = false;
            loop {
                tokio::select! {
                    command = input.recv(), if !commands_closed => match command {
                        Some(AsrCommand::Audio(pcm)) => {
                            if send_done { continue; }
                            if pcm.len() % 2 != 0 { bail!("Tencent ASR needs complete PCM16 samples"); }
                            tokio::time::timeout(Duration::from_secs(5), writer.send(WsMessage::Binary(pcm.into()))).await.context("Tencent ASR write timeout")??;
                        },
                        Some(AsrCommand::Finish) => {
                            if send_done { continue; }
                            tokio::time::timeout(Duration::from_secs(5), writer.send(WsMessage::Text(r#"{"type":"end"}"#.into()))).await.context("Tencent ASR finish timeout")??;
                            send_done = true;
                        },
                        Some(AsrCommand::Close) => return Ok(()),
                        None if send_done => commands_closed = true,
                        None => return Ok(()),
                    },
                    incoming = tokio::time::timeout(Duration::from_secs(30), reader.next()) => {
                        let incoming = incoming.context("Tencent ASR result timeout")?.ok_or_else(|| anyhow!("Tencent ASR closed before final result"))??;
                        let data = match incoming {
                            WsMessage::Text(text) => text.as_bytes().to_vec(),
                            WsMessage::Binary(data) => data.to_vec(),
                            WsMessage::Ping(data) => {
                                tokio::time::timeout(Duration::from_secs(5), writer.send(WsMessage::Pong(data))).await.context("Tencent ASR pong timeout")??;
                                continue;
                            },
                            WsMessage::Close(_) => bail!("Tencent ASR closed before final result"),
                            _ => continue,
                        };
                        let frame: WireFrame = serde_json::from_slice(&data).context("Tencent ASR invalid JSON")?;
                        if frame.code != 0 { bail!("Tencent ASR code {}: {}", frame.code, frame.message); }
                        let recognition = frame.result.unwrap_or_default();
                        let text = recognition.voice_text_str.trim();
                        if !text.is_empty() {
                            if recognition.slice_type == 2 { stable.push_str(text); partial.clear(); }
                            else { partial = text.into(); }
                        }
                        let text = format!("{stable}{partial}").trim().to_string();
                        let final_result = frame.final_ == 1;
                        if (final_result || !text.is_empty()) && out.send(Ok(AsrResult { text, final_result })).await.is_err() { return Ok(()); }
                        if final_result { return Ok(()); }
                    }
                }
            }
        };
        tokio::select! {
            _ = cancel.cancelled() => {}, _ = out.closed() => {},
            result = run => if let Err(error) = result {
                tokio::select! { _ = cancel.cancelled() => {}, _ = out.send(Err(error)) => {} }
            }
        }
    });
    Ok(AsrSession { commands, results })
}

#[cfg(test)]
mod tests {
    use super::super::tests::config;
    use super::*;
    use serde_json::json;

    #[test]
    fn signs_raw_parameters_and_disables_vendor_vad() {
        let mut cfg = config(json!({}));
        cfg.env.extend([
            ("TENCENT_ASR_APP_ID".into(), "1".into()),
            ("TENCENT_ASR_SECRET_ID".into(), "id + /".into()),
            ("TENCENT_ASR_SECRET_KEY".into(), "key".into()),
        ]);
        let url = url::Url::parse(&signed_url(&cfg, "voice +", 1700000000).unwrap()).unwrap();
        let mut params: BTreeMap<String, String> = url
            .query_pairs()
            .map(|(k, v)| (k.into_owned(), v.into_owned()))
            .collect();
        let signature = params.remove("signature").unwrap();
        let source = format!(
            "asr.cloud.tencent.com/asr/v2/1?{}",
            params
                .iter()
                .map(|(k, v)| format!("{k}={v}"))
                .collect::<Vec<_>>()
                .join("&")
        );
        let mut mac = Hmac::<Sha1>::new_from_slice(b"key").unwrap();
        mac.update(source.as_bytes());
        assert_eq!(signature, STANDARD.encode(mac.finalize().into_bytes()));
        assert_eq!(params["needvad"], "0");
        assert_eq!(params["voice_format"], "1");
    }

    #[tokio::test]
    async fn sends_pcm_and_end_then_combines_committed_sentences() {
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let endpoint = format!("ws://{}", listener.local_addr().unwrap());
        let server = tokio::spawn(async move {
            let (stream, _) = listener.accept().await.unwrap();
            let mut ws = tokio_tungstenite::accept_async(stream).await.unwrap();
            assert!(matches!(
                ws.next().await.unwrap().unwrap(),
                WsMessage::Binary(_)
            ));
            ws.send(WsMessage::Text(
                json!({"code":0,"result":null}).to_string().into(),
            ))
            .await
            .unwrap();
            ws.send(WsMessage::Text(
                json!({"result":{"slice_type":2,"voice_text_str":"你好"}})
                    .to_string()
                    .into(),
            ))
            .await
            .unwrap();
            assert_eq!(
                ws.next().await.unwrap().unwrap().into_text().unwrap(),
                r#"{"type":"end"}"#
            );
            ws.send(WsMessage::Text(
                json!({"result":{"slice_type":1,"voice_text_str":"世界"},"final":1})
                    .to_string()
                    .into(),
            ))
            .await
            .unwrap();
        });
        let mut cfg = config(json!({}));
        for key in [
            "TENCENT_ASR_APP_ID",
            "TENCENT_ASR_SECRET_ID",
            "TENCENT_ASR_SECRET_KEY",
        ] {
            cfg.env.insert(key.into(), "fake".into());
        }
        cfg.env
            .insert("TENCENT_ASR_WEBSOCKET_ENDPOINT".into(), endpoint);
        let mut asr = open_asr(cfg, CancellationToken::new()).await.unwrap();
        asr.commands
            .send(AsrCommand::Audio(vec![0; 640]))
            .await
            .unwrap();
        assert_eq!(asr.results.recv().await.unwrap().unwrap().text, "你好");
        asr.commands.send(AsrCommand::Finish).await.unwrap();
        let result = asr.results.recv().await.unwrap().unwrap();
        assert!(result.final_result);
        assert_eq!(result.text, "你好世界");
        server.await.unwrap();
    }
}
