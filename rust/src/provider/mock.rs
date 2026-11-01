use super::*;
use serde_json::json;

pub(super) async fn open_asr(cfg: Config, cancel: CancellationToken) -> Result<AsrSession> {
    tokio::select! {
        _ = cancel.cancelled() => bail!("recognition cancelled"),
        _ = delay(&cfg, "GOLIVE_MOCK_ASR_DIAL_MS", 0.0) => {},
    }
    let (commands, mut input) = mpsc::channel(64);
    let (out, results) = mpsc::channel(32);
    tokio::spawn(async move {
        let run = async {
            let phrase = env(&cfg, "GOLIVE_MOCK_TRANSCRIPT", "你好，帮我查一下明天的天气");
            let mut samples = 0usize;
            let mut last = String::new();
            let voiced_only = cfg
                .secret("GOLIVE_MOCK_ASR_VOICED_ONLY")
                .parse::<bool>()
                .unwrap_or(false);
            let lag = Duration::from_secs_f64(
                env_number(&cfg, "GOLIVE_MOCK_ASR_LAG_MS", 0.0).min(3_600_000.0) / 1000.0,
            );
            let final_delay = Duration::from_secs_f64(
                env_number(&cfg, "GOLIVE_MOCK_ASR_FINAL_MS", 0.0).min(3_600_000.0) / 1000.0,
            );
            let mut pending =
                std::collections::VecDeque::<(tokio::time::Instant, AsrResult)>::new();
            let mut finished = false;
            let mut input_closed = false;
            loop {
                if input_closed && pending.is_empty() {
                    return;
                }
                let deadline = pending
                    .front()
                    .map(|(at, _)| *at)
                    .unwrap_or_else(|| tokio::time::Instant::now() + Duration::from_secs(3600));
                tokio::select! {
                    command = input.recv(), if !input_closed => match command {
                        Some(AsrCommand::Audio(pcm)) if !finished => {
                            if voiced_only {
                                for frame in pcm.chunks(640) {
                                    let energy:f64=frame.chunks_exact(2).map(|b| {
                                        let v=i16::from_le_bytes([b[0],b[1]]) as f64/32768.0; v*v
                                    }).sum();
                                    if energy>(frame.len()/2) as f64*0.0001 { samples+=frame.len()/2; }
                                }
                            } else { samples+=pcm.len()/2; }
                            let text:String=phrase.chars().take(samples/2880).collect();
                            if text!=last && !text.is_empty() {
                                if pending.len()==32 { pending.pop_front(); }
                                pending.push_back((tokio::time::Instant::now()+lag,AsrResult{text:text.clone(),final_result:false}));
                                last=text;
                            }
                        },
                        Some(AsrCommand::Finish) if !finished => {
                            finished=true;
                            let mut at=tokio::time::Instant::now()+final_delay;
                            if let Some((last,_))=pending.back() { at=at.max(*last+Duration::from_millis(1)); }
                            pending.push_back((at,AsrResult{text:last.clone(),final_result:true}));
                        },
                        Some(AsrCommand::Close)=>return,
                        None=>input_closed=true,
                        _=>{},
                    },
                    _ = tokio::time::sleep_until(deadline), if !pending.is_empty() => {
                        let (_,result)=pending.pop_front().unwrap();
                        let done=result.final_result;
                        if out.send(Ok(result)).await.is_err() || done { return; }
                    }
                }
            }
        };
        tokio::select! { _ = cancel.cancelled() => {}, _ = out.closed() => {}, _ = run => {} }
    });
    Ok(AsrSession { commands, results })
}

fn reply(user: &str) -> String {
    if user.trim().is_empty() {
        return "我在，请讲。".into();
    }
    format!(
        "好的，我听到你说{}。让我确认一下细节，然后给你一个完整的答复。这句话故意说得长一点，方便你在中途打断我，测试打断之后的对话历史是否只保留你真正听到的部分。",
        user.trim()
    )
}

pub(super) async fn stream_llm(
    cfg: Config,
    messages: Vec<Message>,
    cancel: CancellationToken,
) -> Result<mpsc::Receiver<Result<LlmDelta>>> {
    let (out, rx) = mpsc::channel(64);
    tokio::spawn(async move {
        let run = async {
            let user = messages
                .iter()
                .rev()
                .find(|m| m.role == "user")
                .map(|m| m.content.as_str())
                .unwrap_or("");
            let mut call = None;
            let answer = if messages.last().is_some_and(|m| m.role == "tool") {
                let tool = &messages.last().unwrap().content;
                format!("查到了，结果是{tool}。")
            } else if user.contains('查') && cfg.string("tool_choice", "auto") != "none" {
                let name = cfg
                    .value("tools")
                    .and_then(Value::as_array)
                    .and_then(|a| a.first())
                    .and_then(|t| t.pointer("/function/name").or_else(|| t.get("name")))
                    .and_then(Value::as_str);
                if let Some(name) = name {
                    call = Some(ToolCall {
                        id: format!("call_{}", uuid::Uuid::new_v4()),
                        name: name.into(),
                        arguments: json!({"request":user}).to_string(),
                    });
                    "好的，我来查一下。".to_string()
                } else {
                    reply(user)
                }
            } else {
                reply(user)
            };
            delay(&cfg, "GOLIVE_MOCK_LLM_TTFT_MS", 0.0).await;
            for ch in answer.chars() {
                if out
                    .send(Ok(LlmDelta {
                        text: ch.to_string(),
                        ..Default::default()
                    }))
                    .await
                    .is_err()
                {
                    return;
                }
                delay(&cfg, "GOLIVE_MOCK_LLM_RUNE_MS", 12.0).await;
            }
            if let Some(call) = call {
                if out
                    .send(Ok(LlmDelta {
                        tool_call: Some(call),
                        ..Default::default()
                    }))
                    .await
                    .is_err()
                {
                    return;
                }
            }
            let _ = out.send(Ok(LlmDelta { usage: Some(json!({"input_tokens":user.chars().count(),"output_tokens":answer.chars().count()})), ..Default::default() })).await;
        };
        tokio::select! { _ = cancel.cancelled() => {}, _ = out.closed() => {}, _ = run => {} }
    });
    Ok(rx)
}

pub(super) async fn synthesize(
    cfg: Config,
    text: String,
    cancel: CancellationToken,
) -> Result<mpsc::Receiver<Result<Vec<u8>>>> {
    let (out, rx) = mpsc::channel(16);
    tokio::spawn(async move {
        let run = async {
            delay(&cfg, "GOLIVE_MOCK_TTS_DIAL_MS", 0.0).await;
            delay(&cfg, "GOLIVE_MOCK_TTS_TTFB_MS", 0.0).await;
            let total = text.chars().count().saturating_mul(1760);
            for offset in (0..total).step_by(1600) {
                let pcm = (offset..(offset + 1600).min(total))
                    .flat_map(|i| {
                        let t = i as f64 / 16000.0;
                        let envelope = 1.0 - (i % 1760) as f64 / 1760.0;
                        let signal = ((t * 440.0 * std::f64::consts::TAU).sin()
                            + (t * 660.0 * std::f64::consts::TAU).sin())
                            * 0.14
                            * envelope;
                        ((signal * 32767.0) as i16).to_le_bytes()
                    })
                    .collect();
                if out.send(Ok(pcm)).await.is_err() {
                    return;
                }
            }
        };
        tokio::select! { _ = cancel.cancelled() => {}, _ = out.closed() => {}, _ = run => {} }
    });
    Ok(rx)
}

#[cfg(test)]
mod tests {
    use super::super::tests::config;
    use super::*;

    #[tokio::test]
    async fn mock_audio_drives_partial_and_final() {
        let mut asr = open_asr(config(json!({})), CancellationToken::new())
            .await
            .unwrap();
        asr.commands
            .send(AsrCommand::Audio(vec![0; 5760]))
            .await
            .unwrap();
        let first = asr.results.recv().await.unwrap().unwrap();
        assert_eq!(first.text, "你");
        assert!(!first.final_result);
        asr.commands.send(AsrCommand::Finish).await.unwrap();
        assert!(asr.results.recv().await.unwrap().unwrap().final_result);
        assert!(asr.results.recv().await.is_none());
    }

    #[tokio::test]
    async fn cancel_closes_stalled_mock_producer() {
        let cancel = CancellationToken::new();
        let mut rx = synthesize(config(json!({})), "a".repeat(200), cancel.clone())
            .await
            .unwrap();
        cancel.cancel();
        tokio::time::timeout(Duration::from_secs(1), async {
            while rx.recv().await.is_some() {}
        })
        .await
        .unwrap();
    }
}
