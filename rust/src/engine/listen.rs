//! Audio framing and per-utterance streaming recognition.
use super::*;

impl Engine {
    pub(super) fn frame(&mut self, pcm: &[u8]) {
        let Ok(samples) = audio::decode_pcm16(pcm) else {
            return;
        };
        self.focus.push(pcm, self.vad.clock_ms() as i64);
        let decision = self.vad.push(&samples);
        if let Some(l) = &mut self.listening {
            l.active_ms = decision.active_ms.max(l.active_ms);
        }
        if decision.kind == DecisionKind::Started {
            if let Some(old) = self.listening.take() {
                old.cancel();
            }
            self.next_id += 1;
            self.listening = Some(Listening {
                id: self.next_id,
                start_ms: decision.start_ms,
                end_ms: 0,
                silence_ms: 0,
                segments: BTreeMap::new(),
                current: None,
                stopped: None,
                barge_in: decision.barge_in,
                yielded: false,
                began: Instant::now(),
                active_ms: decision.active_ms,
            });
            self.emit(json!({"type":"golive.speech.started","start_ms":decision.start_ms,"barge_in":decision.barge_in}));
        }
        if !decision.snapshot.is_empty() && self.listening.is_some() {
            let need_stream = self.listening.as_ref().is_some_and(|l| l.current.is_none());
            // After an early final, silence remains with the VAD. A resumed
            // voice opens a continuation rather than feeding a finished stream.
            if need_stream
                && (decision.kind == DecisionKind::Started || self.vad.trailing_silence_ms() == 0.0)
            {
                self.open_recognition();
            }
            if let Some(listen) = &self.listening {
                if let Some(current) = listen.current {
                    if let Some(r) = listen.segments.get(&current) {
                        if r.commands
                            .try_send(AsrCommand::Audio(audio::encode_pcm16(&decision.snapshot)))
                            .is_err()
                        {
                            self.error(
                                "asr_backpressure",
                                "recognizer cannot keep up with incoming audio",
                            );
                            self.cancel.cancel();
                        }
                    }
                }
            }
        }
        if decision.kind == DecisionKind::Stopped {
            if let Some(l) = &mut self.listening {
                l.end_ms = decision.end_ms;
                l.silence_ms = decision.silence_ms as i64;
                l.stopped = Some(Instant::now());
            }
            self.finish_recognition();
            self.emit(json!({"type":"golive.speech.stopped","start_ms":decision.start_ms,"end_ms":decision.end_ms,"silence_ms":decision.silence_ms as i64}));
            self.finish_utterance_if_ready(false);
        } else if self.cfg.bool("duplex.speculative", true) {
            let early = self.cfg.usize("duplex.early_final_ms", 120);
            if early > 0 && self.vad.trailing_silence_ms() >= early as f64 {
                self.finish_recognition();
            }
        }
    }
    pub(super) fn open_recognition(&mut self) {
        let Some(listen) = &mut self.listening else {
            return;
        };
        self.next_id += 1;
        let segment = self.next_id;
        let utterance = listen.id;
        let cancel = self.cancel.child_token();
        let (tx, mut rx) = mpsc::channel(128);
        listen.segments.insert(
            segment,
            Recognition {
                text: String::new(),
                final_result: false,
                commands: tx,
                cancel: cancel.clone(),
            },
        );
        listen.current = Some(segment);
        let cfg = self.cfg.clone();
        let inner = self.inner.clone();
        tokio::spawn(async move {
            let result: anyhow::Result<()>=async {
                let mut stream=provider::open_asr(cfg,cancel.clone()).await?;
                loop { tokio::select! {
                    _=cancel.cancelled()=>return Ok(()),
                    command=rx.recv()=>match command {
                        Some(command)=>{ tokio::select! { _=cancel.cancelled()=>return Ok(()), r=stream.commands.send(command)=>{r.map_err(|_|anyhow::anyhow!("recognizer closed its input"))?;} } },
                        None=>return Ok(()),
                    },
                    result=stream.results.recv()=>match result {
                        Some(Ok(result))=>{
                            let final_result=result.final_result;
                            tokio::select! { _=cancel.cancelled()=>return Ok(()), _=inner.send(Internal::Recognized{utterance,segment,text:result.text,final_result})=>{} }
                            if final_result {return Ok(());}
                        },
                        Some(Err(e))=>return Err(e),None=>return Ok(()),
                    },
                }}
            }.await;
            cancel.cancel();
            let error = result.err().map(|e| e.to_string());
            let _ = inner
                .send(Internal::RecognitionEnded {
                    utterance,
                    segment,
                    error,
                })
                .await;
        });
    }
    pub(super) fn finish_recognition(&mut self) {
        if let Some(l) = &mut self.listening {
            if let Some(current) = l.current.take() {
                if let Some(r) = l.segments.get(&current) {
                    if r.commands.try_send(AsrCommand::Finish).is_err() {
                        r.cancel.cancel();
                    }
                }
            }
        }
    }
}
