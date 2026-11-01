//! Golden decisions generated from the unchanged Go detector at ad70a1c1a8010877c3a8a130a277e8566ec14ad5.
//! Each digest includes every decision field plus every snapshot/utterance sample bit.
//! The synthetic input exercises gaps, preroll, interruption, patience, forced close,
//! steady noise, and timing configuration changes without external providers.
use golive_rs::vad::{Decision, DecisionKind, Vad, VadConfig};

fn add(hash: &mut u64, number: u64) {
    for byte in number.to_le_bytes() {
        *hash = (*hash ^ u64::from(byte)).wrapping_mul(1099511628211);
    }
}
fn decision(hash: &mut u64, d: Decision) {
    add(hash, d.kind as u64);
    add(hash, d.active_ms.to_bits());
    add(hash, d.start_ms as u64);
    add(hash, d.end_ms as u64);
    add(hash, u64::from(d.barge_in));
    add(hash, d.silence_ms.to_bits());
    for samples in [d.utterance, d.snapshot] {
        add(hash, samples.len() as u64);
        for sample in samples {
            add(hash, u64::from(sample.to_bits()));
        }
    }
}

#[test]
fn vad_matches_go_decisions_and_every_audio_sample() {
    let expected: [(u64, usize); 8] = [
        (0x4a0f7c3276e091f5, 103),
        (0x54f905d3ea8afb9f, 103),
        (0x62412d64f2a7bcf2, 117),
        (0x3b1fe8e5962ede60, 44),
        (0x54131ae909a42388, 128),
        (0x6999aa3153dc99d3, 78),
        (0x4a0f7c3276e091f5, 103),
        (0xcb7b72012d69f375, 95),
    ];
    let stages: [(usize, i32, u8, i32, bool); 15] = [
        (20, 0, 0, 0, false),
        (5, 4000, 0, 0, false),
        (10, 0, 0, 0, false),
        (18, 5000, 0, 0, false),
        (6, 0, 0, 700, false),
        (5, 4000, 0, 0, false),
        (40, 0, 0, 0, false),
        (20, 0, 2, 0, false),
        (20, 500, 0, 0, false),
        (20, 8000, 0, 0, false),
        (20, 0, 1, 0, false),
        (20, 1000, 0, 0, true),
        (30, 0, 0, 0, false),
        (150, 2500, 0, 0, false),
        (30, 0, 0, 0, false),
    ];
    for (index, (expected_hash, expected_events)) in expected.into_iter().enumerate() {
        let mut cfg = VadConfig {
            echo_tail_ms: 600,
            barge_in_margin_db: 16.0,
            ..Default::default()
        };
        match index {
            1 => cfg.speech_pad_ms = 0,
            2 => cfg.min_speech_ms = 80,
            3 => cfg.snapshot_ms = 100,
            4 => cfg.floor_track_ms = -1,
            5 => cfg.max_speech_ms = 900,
            6 => cfg.gap_tolerance_ms = 60,
            7 => cfg.min_silence_ms = 220,
            _ => (),
        }
        let mut vad = Vad::new(cfg);
        let mut hash = 14695981039346656037;
        let mut events = 0;
        for (frames, amplitude, speaking, patience, stop) in stages {
            if speaking != 0 {
                vad.set_assistant_speaking(speaking == 2);
            }
            if patience != 0 {
                vad.set_min_silence_ms(patience);
            }
            let frame: Vec<f32> = (0..vad.frame_samples())
                .map(|i| {
                    (if i / 20 % 2 == 1 {
                        -amplitude
                    } else {
                        amplitude
                    }) as f32
                        / 32768.0
                })
                .collect();
            for _ in 0..frames {
                let d = vad.push(&frame);
                if d.kind != DecisionKind::None {
                    events += 1;
                }
                decision(&mut hash, d);
            }
            if stop {
                decision(&mut hash, vad.stop());
            }
        }
        assert_eq!(events, expected_events, "scenario {index}: event count");
        assert_eq!(
            hash, expected_hash,
            "scenario {index}: sample and decision digest"
        );
    }
}
