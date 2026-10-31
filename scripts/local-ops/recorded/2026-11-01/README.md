# Native transport comparison operations

Historical simulator-only jobs executed through the Mac runner. The Python
files are the exact box-side bodies; shell jobs verify their hashes before
execution. Fixed experiment paths, immutable attempt receipts and simulator
URI checks prevent replaying an already attempted dial. Do not reuse these
files as new jobs: inspect live state and prepare new bounded receipts first.

`native-ab-g711` runs the original A–B–B–A comparison. `native-ab-pcm16`
adds 16 kHz PCM and opening-cache warmup. `native-ab-pcmu` changes only the
latter adapter trial's telephone codec to PCMU, matching the simulator.
Credentials and call content are read privately on the box and are absent
from these files. See the [comparison report](../../../../docs/telephony/2026-11-01-native-sip-comparison.md).

`native-ab-final-control` tests direct G.711/PCMU and repeats the original
Active Call baseline. `native-ab-contact` tests the independent dialog Contact
for 60 seconds with `fb11e2c`; `native-ab-qualification` repeats two full-length
calls with that same candidate. The first attempted Contact job stopped at
its script-hash guard, before reservation or dialing; the archived Contact
job is the corrected staging invocation.

`archive-native-ab.py` uses a temporary loopback Journal to index the isolated
engine log, archive closed calls, and verify manifest hashes and audio channels.
It stops that temporary Journal afterward; it neither dials nor changes the
production Journal. It can be rerun after all experiment receipts are complete.

`finalize-native-ab` verifies the completed recordings and stops only the
isolated engine and owned packet-capture containers. `publish-native-ab-notes`
installs sanitized findings. `import-native-ab-journal` copies verified archives
and simulator pairings into the original Journal, then reloads its same binary,
arguments and environment. It does not restart the original GoLive engine.
The publishing shell job checks both source hashes before either operation.
