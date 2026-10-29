use futures_util::{SinkExt, StreamExt};
use golive_rs::{
    config::Config,
    focus::{Focus, Frame, Tracker},
};
use serde_json::{Value, json};
use std::time::Duration;
use tokio::{net::TcpListener, sync::oneshot};
use tokio_tungstenite::{accept_async, tungstenite::Message};
use tokio_util::sync::CancellationToken;

fn feed(tracker: &mut Tracker, start: i64, frames: usize, active: &[usize]) {
    for index in 0..frames {
        let mut probabilities = vec![0.0; 3];
        for &speaker in active {
            probabilities[speaker] = 0.9;
        }
        tracker.observe(Frame {
            start_ms: start + index as i64 * 80,
            end_ms: start + (index + 1) as i64 * 80,
            probabilities,
        });
    }
}
#[test]
fn first_voice_enrols_and_background_overlap_keeps_the_caller() {
    let mut tracker = Tracker::default();
    feed(&mut tracker, 0, 5, &[0]);
    assert_eq!(tracker.primary(), None);
    feed(&mut tracker, 400, 3, &[0]);
    assert_eq!(tracker.primary(), Some(0));
    let a = tracker.attribute(0, 640);
    assert!(a.known);
    assert_eq!(a.primary_share, 1.0);
    assert_eq!(a.coverage, 1.0);
    feed(&mut tracker, 1000, 12, &[1]);
    let a = tracker.attribute(1000, 1960);
    assert_eq!(a.other_share, 1.0);
    assert_eq!(a.primary_share, 0.0);
    feed(&mut tracker, 2000, 12, &[0, 1]);
    assert_eq!(tracker.attribute(2000, 2960).primary_share, 1.0);
    assert_eq!(tracker.primary(), Some(0));
    assert_eq!(tracker.handovers(), 0);
}
#[test]
fn masks_keep_echo_out_of_enrollment_and_read_barge_in_through_echo() {
    let mut tracker = Tracker::default();
    tracker.mask(0, 2000);
    feed(&mut tracker, 0, 25, &[0]);
    assert_eq!(tracker.primary(), None);
    feed(&mut tracker, 2000, 10, &[1]);
    assert_eq!(tracker.primary(), Some(1));
    assert_eq!(tracker.echo(), Some(0));
    tracker.mask(3000, 7000);
    feed(&mut tracker, 3000, 50, &[0]);
    assert_eq!(tracker.attribute_over(3000, 4000).voiced_ms, 0);
    tracker.mask(7000, 10000);
    feed(&mut tracker, 7000, 10, &[0, 1]);
    let a = tracker.attribute_over(7000, 7800);
    assert!(a.primary);
    assert_eq!(a.voiced_ms, 800);
    feed(&mut tracker, 8000, 10, &[0, 2]);
    assert_eq!(tracker.attribute_over(8000, 8800).other_share, 1.0);
    assert_eq!(tracker.primary(), Some(1));
}
#[test]
fn handover_requires_uninterrupted_other_voice_and_pin_prevents_it() {
    let mut tracker = Tracker::with_handover(2000);
    feed(&mut tracker, 0, 10, &[0]);
    feed(&mut tracker, 800, 20, &[1]);
    assert_eq!(tracker.primary(), Some(0));
    feed(&mut tracker, 2400, 1, &[0]);
    feed(&mut tracker, 2480, 24, &[1]);
    assert_eq!(tracker.primary(), Some(0));
    feed(&mut tracker, 4400, 2, &[1]);
    assert_eq!(tracker.primary(), Some(1));
    assert_eq!(tracker.handovers(), 1);
    tracker.pin(0);
    feed(&mut tracker, 5000, 100, &[1]);
    assert_eq!(tracker.primary(), Some(0));
    assert_eq!(tracker.spoken_since(5000), (0, 8000));
}
#[test]
fn partial_coverage_and_repeated_labels_never_inflate_confidence() {
    let mut tracker = Tracker::default();
    feed(&mut tracker, 0, 10, &[0]);
    assert_eq!(tracker.attribute(500, 1500).coverage, 0.3);
    feed(&mut tracker, 0, 10, &[1]);
    let a = tracker.attribute(0, 800);
    assert_eq!(a.coverage, 1.0);
    assert_eq!(a.other_share, 1.0);
    assert_eq!(a.voiced_ms, 800);
    tracker.observe(Frame {
        start_ms: 790,
        end_ms: 900,
        probabilities: vec![0.9],
    });
    assert_eq!(tracker.labelled_to(), 800);
    tracker.observe(Frame {
        start_ms: 800,
        end_ms: 900,
        probabilities: vec![f32::NAN],
    });
    assert_eq!(tracker.labelled_to(), 800);
}
#[test]
fn retained_history_is_bounded() {
    let mut tracker = Tracker::default();
    feed(&mut tracker, 0, 10000, &[0]);
    assert!(tracker.retained_frames() <= 1126);
    assert_eq!(tracker.attribute(0, 800).coverage, 0.0);
    assert_eq!(tracker.primary(), Some(0));
}

async fn wait_until(mut condition: impl FnMut() -> bool) {
    tokio::time::timeout(Duration::from_secs(2), async {
        while !condition() {
            tokio::time::sleep(Duration::from_millis(5)).await;
        }
    })
    .await
    .unwrap();
}
async fn send(
    socket: &mut tokio_tungstenite::WebSocketStream<tokio::net::TcpStream>,
    value: Value,
) {
    socket
        .send(Message::Text(value.to_string().into()))
        .await
        .unwrap();
}

#[tokio::test]
async fn websocket_binary_audio_enrollment_veto_and_disconnect_fail_open() {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let (release_tx, release_rx) = oneshot::channel();
    let task = tokio::spawn(async move {
        let (socket, _) = listener.accept().await.unwrap();
        let mut socket = accept_async(socket).await.unwrap();
        send(
            &mut socket,
            json!({"type":"ready","lag_ms":100,"frame_ms":100}),
        )
        .await;
        let Message::Binary(audio) = socket.next().await.unwrap().unwrap() else {
            panic!("expected audio");
        };
        assert_eq!(i64::from_le_bytes(audio[..8].try_into().unwrap()), 10000);
        assert_eq!(&audio[8..], &[0, 1, 2, 3]);
        let enrol: Value =
            serde_json::from_slice(&socket.next().await.unwrap().unwrap().into_data()).unwrap();
        assert_eq!(enrol["type"], "enrol");
        assert_eq!(enrol["from_ms"], 0);
        assert_eq!(enrol["to_ms"], 800);
        send(
            &mut socket,
            json!({"type":"enrolled","voiced_ms":800,"tentative":true}),
        )
        .await;
        send(
            &mut socket,
            json!({"type":"frames","start_ms":0,"frame_ms":100,"p":vec![vec![0.9,0.0];8]}),
        )
        .await;
        send(
            &mut socket,
            json!({"type":"frames","start_ms":1000,"frame_ms":100,"p":vec![vec![0.0,0.9];8]}),
        )
        .await;
        release_rx.await.unwrap();
        socket.close(None).await.unwrap();
    });
    let mut cfg = Config::default();
    cfg.set("duplex.focus", json!("veto"));
    cfg.set("duplex.focus_url", json!(format!("ws://{address}/verify")));
    let cancel = CancellationToken::new();
    let focus = Focus::spawn(cfg, cancel.clone());
    wait_until(|| focus.is_ready()).await;
    focus.push(&[0, 1, 2, 3], 10000);
    focus.enrol(0, 800);
    wait_until(|| focus.labelled_to() >= 1800).await;
    assert!(focus.attribute(0, 800).primary);
    assert!(focus.attribute(1000, 1800).veto);
    assert!(!focus.attribute(1000, 3000).veto);
    release_tx.send(()).unwrap();
    task.await.unwrap();
    wait_until(|| !focus.is_ready()).await;
    assert!(!focus.attribute(1000, 1800).veto);
    cancel.cancel();
}

#[tokio::test]
async fn observer_mode_never_vetoes_and_masked_spans_are_not_offered() {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let (done_tx, done_rx) = oneshot::channel();
    let task = tokio::spawn(async move {
        let (socket, _) = listener.accept().await.unwrap();
        let mut socket = accept_async(socket).await.unwrap();
        send(&mut socket, json!({"type":"ready"})).await;
        let _ = socket.next().await.unwrap().unwrap();
        send(
            &mut socket,
            json!({"type":"frames","start_ms":0,"frame_ms":100,"p":vec![vec![0.9,0.0];8]}),
        )
        .await;
        send(
            &mut socket,
            json!({"type":"frames","start_ms":1000,"frame_ms":100,"p":vec![vec![0.0,0.9];8]}),
        )
        .await;
        done_rx.await.unwrap();
        assert!(
            tokio::time::timeout(Duration::from_millis(50), socket.next())
                .await
                .is_err(),
            "masked enrollment should not be sent"
        );
    });
    let mut cfg = Config::default();
    cfg.set("duplex.focus", json!("observe"));
    cfg.set("duplex.focus_url", json!(format!("ws://{address}")));
    let cancel = CancellationToken::new();
    let focus = Focus::spawn(cfg, cancel.clone());
    wait_until(|| focus.is_ready()).await;
    focus.push(&[0; 640], 2000);
    wait_until(|| focus.labelled_to() >= 1800).await;
    let a = focus.attribute(1000, 1800);
    assert!(a.known);
    assert_eq!(a.other_share, 1.0);
    assert!(!a.veto);
    focus.mask(1900, 3000);
    focus.enrol(2000, 2800);
    done_tx.send(()).unwrap();
    task.await.unwrap();
    cancel.cancel();
}

#[tokio::test]
async fn accepted_profile_pins_caller_then_contradiction_disables_veto() {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let (done_tx, done_rx) = oneshot::channel();
    let task = tokio::spawn(async move {
        let (socket, _) = listener.accept().await.unwrap();
        let mut socket = accept_async(socket).await.unwrap();
        send(&mut socket, json!({"type":"ready"})).await;
        let request: Value =
            serde_json::from_slice(&socket.next().await.unwrap().unwrap().into_data()).unwrap();
        assert_eq!(request["profile"], "fixture");
        send(
            &mut socket,
            json!({"type":"enrolled","profile":"fixture","found":true}),
        )
        .await;
        let _ = socket.next().await.unwrap().unwrap();
        send(
            &mut socket,
            json!({"type":"frames","start_ms":10000,"frame_ms":100,"p":vec![vec![0.0,0.9];80]}),
        )
        .await;
        done_rx.await.unwrap();
    });
    let mut cfg = Config::default();
    cfg.set("duplex.focus", json!("veto"));
    cfg.set("duplex.focus_url", json!(format!("ws://{address}")));
    cfg.set("duplex.focus_profile", json!("fixture"));
    let cancel = CancellationToken::new();
    let focus = Focus::spawn(cfg, cancel.clone());
    wait_until(|| focus.is_ready()).await;
    // Allow the profile acknowledgment to reach the reader before advancing the input clock.
    wait_until(|| focus.attribute(0, 1).known).await;
    focus.push(&[0; 640], 18000);
    wait_until(|| focus.labelled_to() >= 18000).await;
    assert!(!focus.acts());
    assert!(!focus.attribute(10000, 18000).veto);
    done_tx.send(()).unwrap();
    task.await.unwrap();
    cancel.cancel();
}

#[tokio::test]
async fn quiet_close_requires_contiguous_labels_and_known_caller() {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let (done_tx, done_rx) = oneshot::channel();
    let task = tokio::spawn(async move {
        let (socket, _) = listener.accept().await.unwrap();
        let mut socket = accept_async(socket).await.unwrap();
        send(&mut socket, json!({"type":"ready"})).await;
        let _ = socket.next().await.unwrap().unwrap();
        send(
            &mut socket,
            json!({"type":"frames","start_ms":0,"frame_ms":100,"p":vec![vec![0.9,0.0];8]}),
        )
        .await;
        send(
            &mut socket,
            json!({"type":"frames","start_ms":800,"frame_ms":100,"p":vec![vec![0.0,0.9];6]}),
        )
        .await;
        done_rx.await.unwrap();
    });
    let mut cfg = Config::default();
    cfg.set("duplex.focus", json!("veto"));
    cfg.set("duplex.focus_url", json!(format!("ws://{address}")));
    let cancel = CancellationToken::new();
    let focus = Focus::spawn(cfg, cancel.clone());
    wait_until(|| focus.is_ready()).await;
    focus.push(&[0; 640], 1400);
    wait_until(|| focus.labelled_to() >= 1400).await;
    assert_eq!(focus.quiet_since(0), Some(800));
    assert_eq!(focus.quiet_since(800), None);
    done_tx.send(()).unwrap();
    task.await.unwrap();
    cancel.cancel();
}
