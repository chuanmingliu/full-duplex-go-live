# Backchannel acknowledgments during caller speech

In the panel, open **Configs → backchannel-service**. New installations seed `enabled` to `true`. Set it to `false` to disable model-generated acknowledgments. Edit `prompt` to choose when GoLive should acknowledge a caller and how it should sound. The model can return `SILENCE` to skip a candidate. Saving creates a config version that new calls read at startup.

While the caller is speaking, GoLive gives the listener model the partial transcript and recent conversation. The acknowledgment has its own synthesis request, audio buffer and playback clock. The main reply has priority. GoLive can discard a candidate if the transcript changes or the caller finishes before playback.

## Reading the full duplex timeline

Group `golive.backchannel.stage` events by `item_id`. The timestamps are milliseconds from engine startup.

| Stage | What the timestamp measures |
| --- | --- |
| `request_started` | The listener requested an acknowledgment using the current partial transcript. |
| `words_delta`, `words_ready` | A text fragment arrived, then the complete acknowledgment became available. |
| `audio_ready`, `released` | Synthesis produced audio, then the playback policy admitted the candidate. |
| `playback_start`, `playback_end` | The engine submitted its first audio frame and estimated the end of its last submitted frame. |
| `skipped` | The candidate ended before playback. The event includes the reason. |

Open the [simulator timeline](../../demo/backchannel/index.html) locally to hear the exchange and compare generated words with audio received by the simulator. The enabled run played two of three candidates. The disabled run generated none. Receiver timestamps measure application audio delivery. Handset playout requires a recording at the handset.
