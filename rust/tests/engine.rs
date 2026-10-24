use golive_rs::{
    config::Config,
    engine::{self, EngineCommand},
};
use serde_json::{Value, json};
use std::time::Duration;
use tokio::time::{Instant, timeout};

fn config() -> Config {
    let mut cfg = Config::default();
    cfg.set("duplex.playback_chunk_ms", json!(20));
    cfg.set("vad.line_fill_ms", json!(0));
    cfg.env
        .insert("GOLIVE_MOCK_TRANSCRIPT".into(), "你好，明天会下雨吗".into());
    cfg
}
async fn until(handle: &mut engine::EngineHandle, kind: &str) -> Vec<Value> {
    timeout(Duration::from_secs(10), async {
        let mut seen = vec![];
        while let Some(event) = handle.events.recv().await {
            let done = event["type"] == kind;
            seen.push(event);
            if done {
                return seen;
            }
        }
        panic!("engine closed before {kind}");
    })
    .await
    .expect("timed out awaiting engine event")
}
#[tokio::test]
async fn typed_turn_streams_audio_then_played_transcript_and_completes() {
    let mut cfg = config();
    cfg.set("duplex.playback_paced", json!(false));
    let mut handle = engine::spawn(cfg);
    handle
        .commands
        .send(EngineCommand::UserText("hello".into()))
        .await
        .unwrap();
    handle.commands.send(EngineCommand::Generate).await.unwrap();
    let events = until(&mut handle, "golive.turn.metrics").await;
    let transcript = events
        .iter()
        .position(|e| e["type"] == "session.output_transcript.delta")
        .unwrap();
    let audio = events
        .iter()
        .position(|e| e["type"] == "session.output_audio.delta")
        .unwrap();
    assert!(audio < transcript);
    assert!(events.last().unwrap()["output_audio_ms"].as_f64().unwrap() > 0.0);
    handle.cancel.cancel();
}
#[tokio::test]
async fn cancelling_playback_prevents_old_turn_audio_from_reappearing() {
    let mut cfg = config();
    cfg.env.insert(
        "GOLIVE_MOCK_REPLY".into(),
        "这是一个需要播放很久的回答，我们用它来检查打断后旧的音频不会重新出现。".into(),
    );
    let mut handle = engine::spawn(cfg);
    handle
        .commands
        .send(EngineCommand::Speak(
            "请听我讲一个比较长的故事，这个故事还没有说完。".into(),
        ))
        .await
        .unwrap();
    let events = until(&mut handle, "session.output_audio.delta").await;
    let old = events.last().unwrap()["item_id"].clone();
    handle.commands.send(EngineCommand::Cancel).await.unwrap();
    let truncated = until(&mut handle, "golive.output_audio.truncated").await;
    assert_eq!(truncated.last().unwrap()["item_id"], old);
    handle
        .commands
        .send(EngineCommand::Speak("新的回答。".into()))
        .await
        .unwrap();
    let next = until(&mut handle, "golive.turn.metrics").await;
    assert!(
        next.iter()
            .filter(|e| e["type"] == "session.output_audio.delta")
            .all(|e| e["item_id"] != old)
    );
    handle.cancel.cancel();
}
#[tokio::test]
async fn odd_pcm_is_rejected_without_killing_the_session() {
    let mut handle = engine::spawn(config());
    handle
        .commands
        .send(EngineCommand::Audio(vec![1]))
        .await
        .unwrap();
    let error = until(&mut handle, "error").await;
    assert_eq!(error.last().unwrap()["error"]["code"], "invalid_audio");
    handle
        .commands
        .send(EngineCommand::Speak("正常。".into()))
        .await
        .unwrap();
    until(&mut handle, "session.output_audio.delta").await;
    handle.cancel.cancel();
}
#[tokio::test]
async fn early_final_prepares_but_does_not_play_before_end_of_turn() {
    let mut cfg = config();
    cfg.set("vad.min_speech_ms", json!(40));
    cfg.set("vad.min_silence_ms", json!(400));
    cfg.set("duplex.early_final_ms", json!(80));
    cfg.set("duplex.semantic_endpointing", json!(false));
    cfg.set("duplex.playback_paced", json!(false));
    let mut handle = engine::spawn(cfg);
    let voiced: Vec<u8> = (0..320)
        .flat_map(|i| (((i as f64 * 0.1).sin() * 8000.0) as i16).to_le_bytes())
        .collect();
    for _ in 0..20 {
        handle
            .commands
            .send(EngineCommand::Audio(voiced.clone()))
            .await
            .unwrap();
    }
    for _ in 0..5 {
        handle
            .commands
            .send(EngineCommand::Audio(vec![0; 640]))
            .await
            .unwrap();
    }
    let deadline = Instant::now() + Duration::from_millis(150);
    while let Ok(Some(event)) = timeout(
        deadline.saturating_duration_since(Instant::now()),
        handle.events.recv(),
    )
    .await
    {
        assert_ne!(
            event["type"], "session.output_audio.delta",
            "speculative audio leaked before VAD close"
        );
    }
    for _ in 0..20 {
        handle
            .commands
            .send(EngineCommand::Audio(vec![0; 640]))
            .await
            .unwrap();
    }
    let events = until(&mut handle, "golive.turn.metrics").await;
    assert!(
        events
            .iter()
            .any(|e| e["type"] == "session.output_audio.delta")
    );
    handle.cancel.cancel();
}
#[tokio::test]
async fn muted_audio_does_not_open_recognition() {
    let mut handle = engine::spawn(config());
    handle
        .commands
        .send(EngineCommand::Mute(true))
        .await
        .unwrap();
    let voiced: Vec<u8> = (0..320)
        .flat_map(|i| (((i as f64 * 0.1).sin() * 8000.0) as i16).to_le_bytes())
        .collect();
    for _ in 0..20 {
        handle
            .commands
            .send(EngineCommand::Audio(voiced.clone()))
            .await
            .unwrap();
    }
    assert!(
        timeout(Duration::from_millis(100), handle.events.recv())
            .await
            .is_err()
    );
    handle.cancel.cancel();
}

#[tokio::test]
async fn disabled_barge_in_finishes_the_current_answer_before_followup() {
    let mut cfg = config();
    cfg.set("duplex.allow_barge_in", json!(false));
    cfg.set("vad.min_speech_ms", json!(40));
    cfg.set("vad.barge_in_min_speech_ms", json!(40));
    cfg.set("vad.min_silence_ms", json!(100));
    cfg.set("duplex.early_final_ms", json!(50));
    cfg.set("duplex.semantic_endpointing", json!(false));
    let mut handle = engine::spawn(cfg);
    handle
        .commands
        .send(EngineCommand::Speak("这是需要播放完毕的第一句。".into()))
        .await
        .unwrap();
    let first = until(&mut handle, "session.output_audio.delta").await;
    let item = first.last().unwrap()["item_id"].clone();
    let voiced: Vec<u8> = (0..320)
        .flat_map(|i| (((i as f64 * 0.1).sin() * 12000.0) as i16).to_le_bytes())
        .collect();
    for _ in 0..20 {
        handle
            .commands
            .send(EngineCommand::Audio(voiced.clone()))
            .await
            .unwrap();
    }
    for _ in 0..10 {
        handle
            .commands
            .send(EngineCommand::Audio(vec![0; 640]))
            .await
            .unwrap();
    }
    let done = until(&mut handle, "golive.turn.metrics").await;
    assert!(
        done.iter()
            .all(|e| e["type"] != "golive.output_audio.truncated")
    );
    assert_eq!(done.last().unwrap()["turn_id"], item);
    let followup = until(&mut handle, "session.output_audio.delta").await;
    assert_ne!(followup.last().unwrap()["item_id"], item);
    handle.cancel.cancel();
}
