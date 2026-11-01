# Call out-mvnxt123-m1tm — 2026-10-25 22:42, build 1f853df, simulated (kb-coverage-nerd)

Through the PBX with the simulated customer, four minutes, the owner's kept
prompt; the first conversation on the trimmed opener.

## Both figures, from the caller's last sound (32 answered turns)

- **First audio** (the opener, said as the pause ends): p50 380, p95 380,
  max 444. 33 answers opened from the cache; the model's own 好的 dropped as
  a duplicate 7 times, said once each time.
- **The answer's own first word** (`first_content_ms`): **p50 887, p90 990,
  p95 990, max 1054**; 2 of 32 over 1000, 0 over 1200.

The opener's rendered pause is cut to 80 ms past its last sound (1f853df),
so the opener is 460–500 ms of audio and no longer decides when the answer
begins (on de62d5f every content figure was exactly 380 + the opener's
length). 

## Focus and hangup

33 judgements, 0 vetoes; 12 barge-ins judged, all the (simulated) caller's,
yielded; 0 quiet closes; 5 speculations abandoned of 33 held. Ended by the
simulated customer.

## What this says

On the phone path (RustPBX, SIP, the simulator as callee) with the real
prompt, both the headline and the content figure are under 1000 at p95 on
this call (n=32) and on the bench run before it (n=22: 969). The evening
hour. A person on the line is still owed; the simulated customer speaks in
clean synthesized sentences.
