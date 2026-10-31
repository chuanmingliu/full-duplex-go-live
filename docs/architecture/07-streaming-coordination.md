# How streaming ASR, LLM and TTS cooperate

[Architecture index](README.md) · [Interactive explorer](explorer.html) · [Conversation components](02-conversation-engine.md)

Implementation reviewed at `9547d22` on 31 October 2026. Configuration values below come from `configs/telephony.json`; they are thresholds and limits, not measured provider response times. Deployment identity is recorded separately in Call Journal and release evidence.

The engine is the coordinator. Tencent recognizes the caller, DeepSeek produces text or tool calls, and MiniMax turns speakable text into audio. These services do not call each other. Go code joins their streams, decides whose turn it is, and owns the output queue. The phone's listening path stays active while the answer plays.

## The four streams

| Stream | What flows | When it can start | What ends or invalidates it |
| --- | --- | --- | --- |
| Caller → ASR | Audio frames in; changing text hypotheses and final text out | When a speech window opens; predial can prepare a connection | End of an ASR run, a timeout, or session close; another run can already be listening |
| Turn → LLM | Conversation/context in; text deltas and structured tool calls out | A qualifying stable pause, an early ASR final, or confirmed final text | Natural completion, provider error, cancellation or superseding turn |
| Speech pipe → TTS | One speakable text segment in; many PCM chunks out | As soon as the first segment is ready, potentially before the turn is committed | Segment completion or cancellation; the connection may be reused |
| Player → phone transport | Ordered, paced PCM with turn ownership | Only eligible output may play; speculative body audio waits for commitment | Natural drain, interruption, replacement or session close |

There are two different kinds of streaming after the LLM: **text is segmented before synthesis**, then **audio streams out of synthesis**. The engine does not issue a new TTS request for every model token. It also does not need to wait for the entire answer before playing its first sentence.

## One ordinary reply, step by step

1. **The caller speaks.** The audio front end decodes and resamples input, keeps a rolling window, and feeds voice activity detection and ASR. Tencent partial text can revise earlier words. The display path exposes a stable prefix where possible, while turn control evaluates the latest hypothesis. Speaker verification and playback-aware checks provide additional evidence about whether the sound belongs to the caller.
2. **A pause opens a preparation window.** The base end-of-turn silence threshold is 380 ms. A complete-looking hypothesis may qualify for speculation after a 100 ms window; the general stable window is 180 ms, with an 80 ms text-settle requirement. These paths have gates: a hesitation, acknowledgement, unfinished thought, stale run or unresolved early-final request can prevent a speculative start. A stable string while someone is still speaking is not sufficient.
3. **ASR finalization can overlap the remaining pause.** After 120 ms of qualifying silence, the engine can ask the current ASR run to finish. It immediately opens a continuation run so resumed speech is still captured. The early result can start preparation; it does not by itself grant permission to speak. The engine can also wait longer—700 ms for a patient pause—when the words appear unfinished.
4. **The LLM starts preparing the answer.** It receives a snapshot of instructions, recent conversation, working memory and available tools. The profile requests eight hedged copies with no initial spacing, selecting a winner once enough initial text is available—four characters—or a tool call/completion qualifies. Only the winning stream continues downstream. Actual parallelism depends on configured lanes/accounts and provider limits; eight requested copies are not eight guaranteed independent resources.
5. **The speech pipe starts TTS before the LLM finishes.** Text deltas enter a buffered pipe. The segmenter looks for a sensible short initial clause, then larger later segments. A first-chunk deadline and short boundary grace reduce waiting for punctuation. TTS starts on that first segment while the LLM can keep producing later text into the buffer.
6. **Commitment releases eligible audio.** If final words agree with the speculative words, the engine commits that turn and keeps its preparation. `confirmed()` holds speculative answer output until then. A cached opener can be emitted through a separate eligible path at the pause; it is not proof that answer content is ready. Every body segment still needs valid turn/generation/revision ownership.
7. **Playback runs while preparation continues.** TTS yields PCM chunks; the player emits paced chunks with a bounded lead. Later text and audio can be prepared while earlier audio plays. Synthesis calls on one TTS stream are sequential, so a slow segment can delay the next one. The LLM's buffered producer can eventually wait behind a slow consumer; this is overlapping bounded work, not unlimited parallel synthesis.
8. **Completion is recorded at playback.** End of model text closes the speech pipe; remaining text is flushed, synthesis finishes, and the player drains. Model completion, TTS completion and audible playback completion are different events. The canonical assistant transcript is announced when a segment begins output, with truncation accounting if it is interrupted.

```mermaid
sequenceDiagram
    participant Caller
    participant E as Go engine / turn control
    participant A as Tencent ASR
    participant L as DeepSeek
    participant S as Segmenter / speech pipe
    participant T as MiniMax
    participant P as Player
    Caller->>E: Audio frames
    E->>A: Stream speech audio
    A-->>E: Partial text, then revisions
    Note over E,A: Qualifying pause; early final and continuation listening can overlap
    E->>L: Prepare from eligible text + context
    L-->>S: Text deltas
    S->>T: First speakable segment
    Note over T,P: TTS may prepare before commitment; body output waits
    A-->>E: Final text agrees
    E->>P: Commit turn / allow eligible output
    T-->>P: First PCM chunks
    P-->>Caller: Paced answer through phone transport
    par Continue the answer
        L-->>S: More text
        S->>T: Later segment when stream is available
        T-->>P: More PCM
    and Keep listening
        Caller->>E: New audio, including interruptions
        E->>A: Continue recognition
    end
```

This diagram shows ordering and overlap, not a measured timing scale. The phone transport and acoustic playback add work after the player callback.

## When the words change

A speculative turn is a provisional answer to a particular transcript. If the caller continues and the recognized words change, the engine abandons that guess. If the final transcript differs, it revises the turn and restarts reasoning using the corrected text. The same visible turn ID can have a new generation and revision; checking only the ID would allow stale audio into a new answer.

Already-played audio cannot be undone. A suitable cached opener may be preserved across a revision, while guessed body text/audio is cancelled. Held speculative events and uncommitted tool calls must not escape as if they were confirmed. The tracker and player therefore carry exact ownership rather than relying on cancellation alone: a provider can return a late chunk after cancellation.

The model history also needs to reflect speech that actually survived an interruption. A fully generated paragraph is not automatically a paragraph the caller heard. The player reports completion or a played prefix so the conversation does not silently treat discarded text as delivered.

## When the caller interrupts

Detection has several stages. Voice activity first notices sound; recognition and caller verification help distinguish a new request from noise, echo or a short acknowledgement. The configured acknowledgement hold is 600 ms. During that judgement the engine can retain the answer instead of restarting it on every sound. These policies mean the moment sound begins and the moment the system yields are different.

For a genuine new query, this profile uses `on_new_query: cut`. The engine cancels the old model/speech work, invalidates its generation and interrupts queued playback. The new caller audio keeps reaching ASR. A `finish_sentence` policy exists for other configurations, but it is not the checked phone policy.

Cancelling TTS has two responsibilities: the caller cancels the specific synthesis context, and the provider adapter drains or resynchronizes its persistent connection before reuse. Otherwise the tail of an old sentence could be mistaken for the beginning of the next one. `reset_tts_on_interrupt: false` does not mean old audio is allowed to leak; it means the whole shared session is not unconditionally reset by that option.

An opener can become a listening backchannel when the caller continues. The `9547d22` correction permits that only for the exact active generation **before answer body output has started**. Once content has started, ordinary interruption accounting must retain its timing and truncated output. The older behaviour could erase a started answer from the measured population.

## When the LLM requests a quotation

Tool calls are structured output, not speech tokens. After the backend stream finishes and its turn is confirmed, the engine dispatches eligible calls. The asynchronous tool coordinator can keep the conversation alive while CRM performs the quote operation. Interrupting speech does not automatically cancel an already-running business task.

When a result becomes eligible to speak, a fixed readout can enter the speech pipe ahead of model-generated follow-up text. Matching prepared quotation PCM may bypass a fresh TTS call for that segment. The exact current result, selection and voice must match the cache entry. A cache miss takes the normal synthesis path. Generic acknowledgements, result-ready phrases and actual price readout remain separate events; a prompt acknowledgement must not be reported as a prompt quotation.

See [Business and quotations](03-business-and-quotations.md) for memory, coalesced requests, result ownership and prepared-audio boundaries.

## What controls latency

| Setting in the checked profile | Value | What it affects |
| --- | --- | --- |
| VAD frame / base silence | 20 / 380 ms | Granularity and normal turn-end waiting |
| Early-final request | 120 ms | When a qualifying pause can close one ASR run while another listens |
| Speculative eager / stable / settle | 100 / 180 / 80 ms | Conditional preparation gates, not three delays added together |
| Patient silence | 700 ms | Extra patience for an apparently unfinished thought |
| LLM hedge copies / start spacing / qualifying characters | 8 / 0 ms / 4 | Race for usable initial model output |
| First text chunk / deadline / boundary grace | 4 characters / 100 / 30 ms | Initial speakable segment; a too-short fragment may defer the deadline |
| Later text chunk minimum / maximum | 24 / 120 characters | Chunking targets with punctuation and safety rules; not guaranteed lengths |
| TTS hedge copies / delayed hedge / fallback | 2 / 260 / 320 ms | Conditional alternatives for slow startup; bounded by account/rate policies |
| Playback chunk / lead | 40 / 300 ms | Emission cadence and how far audio can be queued ahead |

Reducing a threshold can make the system start sooner but also increase false turn endings, fragmented prosody or wasted speculative work. A longer player lead can smooth delivery while leaving more audio already downstream when the caller interrupts. The system cannot retract audio already in a carrier or handset buffer.

For an ordinary answer, the first-content critical path is roughly: the later of **turn commitment** and **first body PCM readiness**, followed by any queued output and downstream transport. PCM readiness itself depends on recognition usable enough to start, model startup, a speakable text segment, synthesis startup, and possible connection/rate waits. Because these overlap, adding every configured timer is not an end-to-end latency estimate. Cached openers and prepared quotations follow different paths.

## Reading the evidence

| Evidence | Boundary it actually describes |
| --- | --- |
| ASR partial/final events | Recognition progress; not necessarily the physical end of speech |
| `think: first token` | Text reaching the engine from the winning backend stream |
| First segment / TTS first audio | Text submitted and synthesized audio becoming available |
| First audio output | First eligible emitted PCM, which may be an opener |
| First content output | First emitted body content for the answer |
| First quotation readout | First eligible readout audio linked to the originating quote request |
| Truncation/completion report | What the player's clock accounts as played or drained |
| One-clock acoustic handset recording | Caller speech end to phone loudspeaker response at the measurement location |

Call Journal's server recording and timestamps help diagnose every internal stage. They do not include all carrier buffering, handset decoding and acoustic playback. The below-1200 ms physical handset goal therefore remains a separate measurement requirement.

## Implementation map

| Concern | Source |
| --- | --- |
| Provider streaming contracts | [provider.go](../../internal/provider/provider.go) |
| ASR runs, early final, speculation, commitment | [listen.go](../../internal/duplex/listen.go) |
| Concurrent backend stream and tool-call dispatch | [think.go](../../internal/duplex/think.go) |
| Speech pipe, TTS, floor hold, interruption and timing | [engine.go](../../internal/duplex/engine.go) |
| Text boundaries and normalization | [sentence.go](../../internal/segment/sentence.go) |
| Turn ownership and provisional events | [turn.go](../../internal/duplex/turn.go) |
| Ordered output and exact ownership | [playback.go](../../internal/duplex/playback.go), [playback_owner.go](../../internal/duplex/playback_owner.go) |
| Recognition transport | [Tencent adapter](../../internal/provider/tencent) |
| Winning model stream | [DeepSeek adapter](../../internal/provider/deepseek), [hedge.go](../../internal/provider/hedge.go) |
| Persistent synthesis and alternatives | [MiniMax TTS](../../internal/provider/minimax/tts.go), [hedge_tts.go](../../internal/provider/hedge_tts.go), [fallback_tts.go](../../internal/provider/fallback_tts.go) |
| Phone configuration | [telephony.json](../../configs/telephony.json) |
| Interrupted-answer regression | [opener_content_test.go](../../internal/duplex/opener_content_test.go) |
