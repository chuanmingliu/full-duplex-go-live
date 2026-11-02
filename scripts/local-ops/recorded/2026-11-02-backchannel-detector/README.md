# Backchannel observer deployment operations

Historical jobs, executed through the existing Mac runner on 2026-11-02.
Do not replay them: they bind the original `f1fd9a6` process/profile, Aria
revision 13, a free loopback port and a unique release/receipt directory.

- `inventory.job`: read-only CPU, memory, Docker/Compose and service inventory.
- `deploy.py` / `deploy.job`: verify payload and original service state, build
  the independent image, run tests, start the observer, replay private audio
  locally, stop/restart only the observer and verify preservation.
- `verify.job`: confirm Docker health, model readiness, resource limits,
  loopback binding and the original engine's health.

The deployment leaves the original backchannel controller authoritative.
No phone service is restarted, no routing/profile changed and no real call
placed. Cleanup on a failed attempt stops only the newly created observer.
The separately staged archive contains public service code and pinned model
artifacts, not credentials or customer recordings. Model binaries are excluded
from Git; use the service's checksum-verifying downloader to obtain them.

The manifest records executed script hashes, the original payload hash and
every deployed source/model hash. Private replay uses a verified archived WAV
on the box and exports only numerical aggregate results. Port 18087 remains
loopback-only; no public tunnel or LAN endpoint was created.
