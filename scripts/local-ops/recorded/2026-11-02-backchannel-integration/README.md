# Backchannel integration operations

Reviewed operation bodies for the `60dbe53` Active Call trial. They are bound to
specific build, configuration and attempt identities and must not be replayed.
Use the existing Mac runner; large bodies require `stage-job.py` wrappers.

- `deploy.sh` requires successful full CI, idle phone/demo services, original
  configuration and the known running executable. It saves the original
  executable, profile and private launch material before adding only the
  detector URL and `asr_assist` mode. It restarts the phone process under the
  existing supervisor, verifies warming, and preserves other services,
  archives and the earlier Aria restore anchor.
- `simulate.sh` runs the existing `set-a-01-barge-in` scenario through Active
  Call with the saved original conversation settings. It verifies both audio
  directions and transcript speakers, detector streaming and absence of
  fallback, and reports normal and quotation latency separately.
- `real-call.job` requires the verified deployment and simulator, unchanged
  original conversation settings, healthy recording and an idle phone. It
  places exactly one owner-requested real call and retains its returned ID.
  An unknown dial outcome must be inspected, never automatically redialed.

The manifest fingerprints the exact bodies. Private credentials, prompts,
raw call data, release binaries and process environments are excluded.
Execution outcomes belong in the corresponding deployment record; preparing
these scripts alone does not prove deployment or a completed test call.

The first simulator exposed an initial pre-answer mute disabling the model
before any audio. `diagnose-initial-mute.job` inspected only bounded detector
metadata. The `*-premute-fix.*` bodies target the corrective `7a466e3` build;
the second deployment preserves the already-enabled profile byte-for-byte.
The original unmodified profile remains in the first release backup.

The second simulator reached the model but hit `queue_overflow` after 30
chunks. `diagnose-queue-overflow.job` records this explicitly. The burst-fix
bodies target `b51e959`; `model-burst-smoke.job` verifies sequential input followed
by a 640 ms batch against the actual detector without dialing. The final
real-call body requires the burst-fix deployment and simulator receipts.
