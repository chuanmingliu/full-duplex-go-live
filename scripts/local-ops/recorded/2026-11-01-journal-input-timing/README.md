# Experiment Journal: input provenance and separate conversation timing

This operation upgrades only the experiment Journal on `192.168.71.9:9916`
to a binary built from clean commit `302267b`. `ci.json` records successful
full CI for that exact commit. The guarded operation waits for both retained
simulator attempts to be terminal and verified; it never dials.

The release backs up the old executable, retains the exact running arguments,
environment and working directory, and records both process IDs. It checks
all prior call IDs, raw audio/transcript/session/log hashes and archive manifest integrity, original config hashes, Active
Call identity, 365-day retention and known caller-input timing evidence.
A failed verification restores and restarts the old Journal executable.

The existing Journal display target stays 1200 ms. Qualification reports use
1150 ms separately to allow for the owner's assumed 50 ms overhead. The new
UI separates conversational first content from quote-only responses and exposes
validated caller-input origin evidence. It does not convert server timing into
a physical handset measurement. Original Journal 8790 and native Journal 9896
are unchanged; the experiment detail view serves this updated reader.

`manifest.json` binds reviewed payloads. Do not replay a completed release;
inspect its unique receipt instead. No credentials or raw call data are stored
in this directory.

The first operation rolled back automatically: it incorrectly required the
whole manifest to be byte-identical. `Archive.Put` intentionally rewrites
`call.json` and updates the manifest when the live log is re-indexed by a newer
parser. The original scripts, manifest and rollback receipt are retained.
The v2 operation preserves checks on every raw/transcript file, backs up derived
metadata, and validates every new manifest entry plus stable call/session/build
identity. It exited 0 at 14:53:35: 12 calls retained, 48 raw/transcript files
unchanged, all 12 manifests valid, and 12 derived summaries updated. Running
Journal PID is 1123992; `receipt.json` records the verified binary hash.
