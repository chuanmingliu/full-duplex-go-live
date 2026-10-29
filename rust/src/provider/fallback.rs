use super::*;
use anyhow::{Context, anyhow};

async fn first_audio(source: &mut mpsc::Receiver<Result<Vec<u8>>>) -> Result<Vec<u8>> {
    while let Some(chunk) = source.recv().await {
        let chunk = chunk?;
        if !chunk.is_empty() {
            return Ok(chunk);
        }
    }
    bail!("synthesizer completed without audio")
}

pub(super) fn race(
    primary: Synthesizer,
    secondary: Synthesizer,
    text: String,
    wait: Duration,
    cancel: CancellationToken,
) -> mpsc::Receiver<Result<Vec<u8>>> {
    let (out, rx) = mpsc::channel(16);
    tokio::spawn(async move {
        let primary_cancel = cancel.child_token();
        let secondary_cancel = cancel.child_token();
        let _primary_guard = primary_cancel.clone().drop_guard();
        let _secondary_guard = secondary_cancel.clone().drop_guard();
        let run = async {
            let failed = tokio::sync::Notify::new();
            let primary_first = async {
                let mut source = primary
                    .enqueue(text.clone(), primary_cancel.clone())
                    .await?;
                let first = first_audio(&mut source).await?;
                Ok::<_, anyhow::Error>((first, source))
            };
            let secondary_first = async {
                tokio::select! { _=tokio::time::sleep(wait)=>{}, _=failed.notified()=>{} }
                let mut source = secondary
                    .enqueue(text.clone(), secondary_cancel.clone())
                    .await?;
                let first = first_audio(&mut source).await?;
                Ok::<_, anyhow::Error>((first, source))
            };
            tokio::pin!(primary_first, secondary_first);
            let (first, mut source) = tokio::select! {
                result=&mut primary_first=>match result {
                    Ok(value)=>{ secondary_cancel.cancel(); value },
                    Err(primary_error)=>{
                        failed.notify_one();
                        primary_cancel.cancel();
                        secondary_first.await.with_context(||format!("primary synthesis failed: {primary_error}; fallback also failed"))?
                    }
                },
                result=&mut secondary_first=>match result {
                    Ok(value)=>{ primary_cancel.cancel(); value },
                    Err(secondary_error)=>{
                        secondary_cancel.cancel();
                        primary_first.await.with_context(||format!("fallback synthesis failed: {secondary_error}; primary also failed"))?
                    }
                }
            };
            out.send(Ok(first))
                .await
                .map_err(|_| anyhow!("audio consumer closed"))?;
            while let Some(chunk) = source.recv().await {
                out.send(chunk)
                    .await
                    .map_err(|_| anyhow!("audio consumer closed"))?;
            }
            Ok::<_, anyhow::Error>(())
        };
        tokio::select! {
            _=cancel.cancelled()=>{}, _=out.closed()=>{},
            result=run=>if let Err(error)=result {
                tokio::select! { _=cancel.cancelled()=>{}, _=out.send(Err(error))=>{} }
            }
        }
    });
    rx
}

#[cfg(test)]
mod tests {
    use super::super::tests::config;
    use super::*;
    use serde_json::json;

    #[tokio::test]
    async fn fallback_starts_after_primary_fails_without_waiting_for_delay() {
        let primary = Synthesizer::new(config(json!({"tts":"unknown"})));
        let secondary = Synthesizer::new(config(json!({"tts":"mock"})));
        let mut rx = race(
            primary,
            secondary,
            "a".into(),
            Duration::from_secs(30),
            CancellationToken::new(),
        );
        let first = tokio::time::timeout(Duration::from_secs(1), rx.recv())
            .await
            .unwrap()
            .unwrap()
            .unwrap();
        assert!(!first.is_empty());
    }
}
