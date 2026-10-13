# Dedicated Active Call receiving adapter

This directory builds a new image from Active Call commit
`eaf7ebc8f39193a289348989f6a09b0ace287da7` (0.3.100). It never modifies an
installed gateway. The source pin, immutable base-image digests, and committed
`Cargo.lock` define the build. Upstream omits its lockfile, so this package owns
one; `lock-export` is an explicit maintenance target, not part of normal builds.

`handshake.patch` creates the realtime request through `IntoClientRequest`
before adding provider-specific headers. The original bare HTTP request lacks
required WebSocket handshake headers. Custom URLs still receive no auth header;
the simulator supplies `run`, `call`, and an unguessable `token` in the URL.
The patch also removes that URL from the connection info log. Run at warning log
level and keep event dumping disabled; do not enable debug logging with secrets.

The default runtime config enables only PCMU, a twenty-port RTP range, no
recording, no automatic transfer, and no providers or registration. HTTP and SIP
are for the dedicated isolated network; the enclosing Compose file controls
host exposure. Runtime config and webhook credentials belong in the simulator's
private volume. `active-call.example.toml` is a template with a deliberately
invalid token. The optional `render_config.py` writes a new mode-0600 config
from a token file and refuses to overwrite an existing file.

## Exact identity and lifecycle

- Prepare a run, then direct the independent test peer to
  `sip:<exact-run-id>@simulator.invalid`. Run IDs already start with `sim-`;
  never add another prefix. `SIM_SIP_DOMAIN` must match the URI host, including
  a port if the URI has one. Fixed-number carrier routing is unsupported until
  a trusted gateway supplies an equally explicit reservation association.
- Native POST `/active-call/incoming` requires its dedicated Bearer token.
  Only `event: invite` is accepted. It attaches `/call/sip?id=<dialogId>` and
  starts reading before the controller schedules answer delay.
- Native `dialogId` is kept internally; the simulator `CallID` is
  `ac-` plus SHA-256 of that exact native ID. SIP Call-ID is never substituted.
  The extension supplies RunID; no callback binds to a current/latest run.
- Accept is written at most once. Its `option.realtime.provider` is the exact
  simulator WebSocket URL and its secret key is a local placeholder, preventing
  ambient provider credentials. Only an `answer` event confirms signaling.
- Caller CANCEL arrives as `hangup` / `Canceled` / `caller`. It is marked
  terminal immediately, so a pending Accept fails. Socket closure is a failed
  call, not proof that SIP ended. An exact-ID REST Hangup is attempted by the
  controller cleanup if the control socket is lost. Uncertain Accept is never
  retried or replaced by a new dial.
- Native dialogs retain bounded tombstones (4096 per process). Duplicate
  callbacks cannot resurrect a terminal call or claim a later reservation.
  After an attachment failure the original dialog is not retried; prepare a new
  run and make a new explicitly authorized test call.

## Independent local SIP fixture

`sip_fixture.py` uses Python 3.11+ stdlib. Run it only on the new simulator
network with the prepared run ID. It sends one INVITE, ACK, synthetic PCMU
tone/silence while receiving RTP concurrently, and BYE; it performs no retry.
Its host allowlist is `activecall`, `localhost`, or `127.0.0.1`, and resolved
targets must be private. `--cancel-after-ms 1000` instead exercises pre-answer
CANCEL. Example arguments for the dedicated fixture container:

```text
python /fixtures/sip_fixture.py --allow-isolated-sip \
  --target-host activecall --target-port 25060 \
  --sip-domain simulator.invalid --run-id sim-<24-hex-digits> --duration 8
```

The JSON result is labeled `isolated_sip_synthetic_pcmu` and counts sent RTP,
received RTP, and non-silent received RTP. Passing it proves a local SIP/media
path with synthetic audio. It does not prove carrier routing, audible natural
language, provider quality, or handset acoustics.

The Go suite also tests native wire fixtures with a local WebSocket gateway,
including delayed admission, cancellation at 1499 ms before a 1500 ms answer,
duplicate webhook, stale run, encoded native IDs, transfer-track exclusion,
command payload fields, authentication, body limits, and control loss.
