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
  never add another prefix. The URI authority may match `SIM_SIP_DOMAIN`, or
  identify the gateway hostname from `SIM_ACTIVECALL_URL` or one of its resolved
  IP addresses, with or without a numeric SIP port. This permits a PBX to forward
  `sip:<exact-run-id>@172.20.0.2:25060` without changing the logical SIP domain.
  DNS lookup is bounded and restricted to the configured gateway hostname;
  unrelated hosts are rejected. `SIM_ACTIVECALL_URL` should name the gateway
  directly: if it names a reverse proxy, automatic aliases cover that proxy,
  and a distinct gateway address still needs an explicit `SIM_SIP_DOMAIN`.
  Fixed-number carrier routing is unsupported
  until a trusted gateway supplies an equally explicit reservation association.
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

## RTP source address and latching

The pinned Active Call source enables symmetric RTP latching by default
([configuration](https://github.com/miuda-ai/active-call/blob/eaf7ebc8f39193a289348989f6a09b0ace287da7/src/config.rs#L91),
[RTP track setup](https://github.com/miuda-ai/active-call/blob/eaf7ebc8f39193a289348989f6a09b0ace287da7/src/media/track/rtc.rs#L278)).
The committed lockfile selects `rustrtc 0.4.7`; its downloaded crate checksum was
verified against that lockfile during the 2026-10-16 audit.

For plain SIP/RTP, a source IP/port that differs from SDP is not rejected merely
for that difference. With the default zero probation, the first eligible RTP
header latches the full observed source IP and port; the SDP SSRC must match if
one was supplied. Subsequent outgoing RTP uses that address
([receive/latch](https://github.com/restsend/rustrtc/blob/b89dc2c4edf96b872b8114689fcc483dec318c58/src/transports/ice/conn.rs#L574),
[immediate update](https://github.com/restsend/rustrtc/blob/b89dc2c4edf96b872b8114689fcc483dec318c58/src/transports/ice/conn.rs#L699),
[send path](https://github.com/restsend/rustrtc/blob/b89dc2c4edf96b872b8114689fcc483dec318c58/src/transports/ice/conn.rs#L368)).
Thus SDP `192.168.71.9` and an actual bridge source of `172.20.0.1` are supported.

Before the first eligible inbound RTP packet, outgoing media still uses SDP.
RTCP does not retarget RTP, and later source addresses do not continually move
the learned destination; signaling can reset it. Address learning is not peer
authentication, so the gateway remains on its dedicated private network.
This is verification of the **pinned source**, not a new runtime test of the
deployed image or NAT path. The reported live-call counts of 163 / 60 bridge
packets demonstrate flow but alone do not prove which latching path ran.

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
