# Delivered repeated requests must not block the next ready result

The recorded quotation diagnostic `sim-quotephases-916ac344-59ab-4e0b-a527-2fd393c75a40`
left task t8 queued despite its 1 ms execution. Its calling turn completed, but
a continuation for an earlier repeated request remained in front of its
acknowledgment. The covering task t7 had already been delivered.

The scheduler now records and retires complete continuations covered entirely
by one already-delivered, uncancelled task before examining the next ready
acknowledgment. This is bookkeeping: it starts no additional generation and
creates no playback receipt. Running, cancelled, mixed, incomplete and scripted
continuations remain ordered. Caller speech still prevents result delivery.

The regression fails on `34ce33f` and passes with this change. The focused race
suite passed in 23.006 seconds. An identical opt-in scheduler simulation held an
unrelated answer for 3000 ms and preserved the configured 1500 ms result wait:
baseline ready-to-delivery was **3010 ms**, candidate **1504 ms**. This isolates
scheduling, not ASR, TTS, quotation computation or physical handset latency.

The original provider configuration, quotation service delay and Active Call
rollback remain unchanged. Recorded SIP validation is pending. Quotation
completion is reported separately under the owner's accepted policy; the
conversational gate still requires p95 <1150 ms and at least 20 valid samples.
Two silent originating turns in the earlier diagnostic still lack playback
records; they must not be fabricated or silently treated as verified links.
