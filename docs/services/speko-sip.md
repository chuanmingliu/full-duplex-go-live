# SIP → Speko → GPT-Live

The existing SIP adapter can now use the Speko service end to end:

```text
Handset / PBX ↔ SIP + G.711 RTP ↔ golive-telephony
  ↔ Realtime/G.711 protocol bridge ↔ Speko /v1/live ↔ gpt-live-1
```

Both PCMA and PCMU stay at 8 kHz on the SIP side. The bridge converts to
24 kHz PCM16 for Speko, whose live Router rejects G.711 upstream. Conversion
preserves partial samples across WebSocket chunks and keeps the 20 ms clock. The
bridge is available in `golive-speko` and as opt-in routes in `golive`.
This feature has been tested with local SIP peers and a mock Router. A live no-phone probe verified non-silent GPT-Live output on 2026-10-31.
Real handset latency still requires an explicitly initiated call and measurement.

## Add the key on Mac or ThinkPad

Run from the **updated source checkout** on the host that will run the bridge:

```sh
python3 scripts/configure_speko.py
```

Paste the key at the hidden prompt. The script creates or updates
`$HOME/.config/golive/speko.env` with permissions `600`. It preserves unrelated
settings and the existing local auth token, generates that token if absent,
and sets `GOLIVE_ADAPTER_TOKEN` to match it. The Speko key never appears in
command arguments or shell history. The command works in both bash and zsh;
Python 3 is required. Nothing is deployed, restarted or dialed by this script.

For a different dedicated credential file:

```sh
python3 scripts/configure_speko.py --env /absolute/private/path/speko.env
```

Keep this file private and on the host running the service. An adapter on
another machine needs the matching **local adapter token**, not the Speko key.
Shell environment variables override dotenv values; restart the bridge after
updating the file, and remove stale exported variables if they override it.

## Recommended: dedicated standalone bridge

From the checkout on either machine:

```sh
go build -o bin/golive-speko ./cmd/golive-speko
go build -o bin/golive-telephony ./cmd/golive-telephony
bin/golive-speko -env "$HOME/.config/golive/speko.env" \
  -addr 127.0.0.1:8095 -max-sessions 1
```

Run it in its own terminal or under your chosen process supervisor. SIGTERM
closes the active call's model and client sockets; restart only when idle.
The existing phone service is not replaced. On the ThinkPad, the older release
bundle does not contain this new binary/script until it is explicitly updated.

Copy your existing **private** SIP adapter configuration and change these fields:

```json
{
  "golive_url": "http://127.0.0.1:8095",
  "golive_api_base": "/v1"
}
```

These are fields to merge into the adapter config, not a complete config. Keep
the correct PBX address, allowed destination, media CIDRs, dedicated SIP/RTP
ports and private record directory. If starting from the checked-in SIP example,
replace all placeholder addresses/destinations and choose a writable record
directory first. A Mac can run the bridge; reaching the PBX requires appropriate
LAN connectivity, concrete advertised addresses and matching media allowlists.

Check readiness without dialing:

```sh
bin/golive-telephony -env "$HOME/.config/golive/speko.env" \
  -config /absolute/private/path/sip-speko.json
```

When you intentionally want one real call:

```sh
bin/golive-telephony -env "$HOME/.config/golive/speko.env" \
  -config /absolute/private/path/sip-speko.json \
  -call /absolute/private/path/call.json -dial
```

Use a new call ID for each deliberate attempt. The existing adapter lock,
destination allowlist, call-duration limit and recording behavior still apply.
No real call has been placed as part of implementing this feature.

## Embedded GoLive option

Enable `GOLIVE_SPEKO_ENABLED=true` and run a dedicated GoLive profile with
`max_sessions: 1`, using the same credential file. Its existing provider
preflight still applies, so use a mock-provider profile if the only desired
remote provider is Speko. The bridge always holds for opening independently
of the local engine's Realtime settings.

Set the private adapter config's `golive_url` to that GoLive HTTP origin and
`golive_api_base` to `/v1/speko`. For example, for GoLive on port 8080:

```json
{
  "golive_url": "http://127.0.0.1:8080",
  "golive_api_base": "/v1/speko"
}
```

| Operation | Standalone | Embedded |
| --- | --- | --- |
| Realtime bridge | `/v1/realtime?call=CALL_ID` | `/v1/speko/realtime?call=CALL_ID` |
| Idle/capacity check | `/v1/pool` | `/v1/speko/pool` |
| Answer release | `POST /v1/opening` | `POST /v1/speko/opening` |
| Native GPT-Live relay | `/v1/live` | `/v1/speko/live` |

All call and control endpoints require the local bearer token. The call ID is
local correlation data; it is never sent to Speko as a query. Pool readiness
checks configuration and capacity, not provider balance or model access.

## Protocol behavior

The bridge sends `session.created` and accepts the adapter's `session.update`
with instructions, voice and matching `g711_alaw` or `g711_ulaw` formats. It
acknowledges the held session before SIP INVITE, but creates no Speko session
until `/v1/opening` releases the exact call ID after SIP answer. A second release
is rejected. A missing answer expires after 180 seconds. An upstream failure
after answer reaches the adapter as an error and causes SIP teardown.

On release, it starts `gpt-live-1` with PCM audio, the instructions and
voice. Responses delegation uses `SPEKO_RESPONSES_MODEL` (default
`gpt-6-luna`). Account access to that backend must also be available.

For the tested Speko route, run the Mac bridge with
`SPEKO_DELEGATION_MODE=client`. Managed `responses` delegation returned
`provider_unavailable`, while standalone voice sessions and the separate text
endpoint both worked. In client mode the bridge keeps bounded transcript history,
handles `session.delegation.created`, and calls Speko's `/v1/llm/responses` with
`gpt-6-luna`. It returns a completed, short result using the original delegation
ID. Duplicate IDs are ignored; newer work cancels superseded work, and disconnect
cancels HTTP work. This bridge has no business tools and cannot make bookings or
retrieve private records. Backend failures are reported honestly to the caller.
The default remains `responses` for routes where managed delegation is available.
 The opening
is seeded in session context and cued after `session.started`; it may be
paraphrased. Opening HTTP success acknowledges release, not audible delivery.
See the [GPT-Live delegation and greeting contract](https://developers.openai.com/api/docs/guides/live-delegation).

Caller audio maps to `session.input_audio.append`; model audio maps to Realtime
audio deltas. Caller chunks are forwarded immediately because RTP already paces input;
input gaps are filled with codec silence on a 20 ms clock. This drains packets
that accumulated during the upstream handshake without adding a permanent delay. Output
is paced at 20 ms, with a two-second bounded queue and final partial-frame
padding. The adapter retains its own RTP pacing and smaller queue limit.

GPT-Live has no per-turn audio-done event. The bridge creates local response IDs
and emits Realtime completion after a 200 ms audio gap with an empty output queue,
marked `bridge_boundary: audio_idle`. This is transport bookkeeping, not proof
that a semantic turn or handset playback completed. Input transcript fragments
are recorded as deltas rather than invented finalized utterances.

The voice model owns natural interruption handling. An explicit Realtime
`response.cancel` clears pending playback, sends a native stop instruction,
and suppresses the current output burst until a 200 ms gap or digital silence. It does not cancel
managed backend work. PCM, client VAD settings, custom tools, format changes,
conversation item commands and other unsupported Realtime operations fail
explicitly. Local GoLive business tools are not run by this bridge.

The native upstream contract comes from
[Speko speech-to-speech](https://docs.speko.ai/relay/s2s).

## Verification

```sh
go test -race ./internal/speko ./internal/telephony ./internal/server ./internal/config
python3 -B scripts/test_configure_speko.py
```

The end-to-end fixtures exercise the actual adapter and bridge with local UDP
SIP/RTP peers: PCMA through the standalone service, PCMU through embedded
GoLive, answer-gated setup, caller audio, an 800 ms output burst, remote BYE and
upstream cleanup. Additional tests cover duplicate releases, hold timeout,
credential-safe errors, cancellation and shutdown during a stalled upgrade.

## Prepared connection and local interruption control

For the isolated Mac SIP bridge, `SPEKO_PRECONNECT=1` prepares the provider
session during ringing without sending caller audio or a greeting. The named
opening release still gates both on SIP answer. Provider session billing may
start during ringing. An unanswered/cancelled call closes the prepared socket.

`SPEKO_LOCAL_BARGE_IN=1` enables a 120 ms receive-leg energy gate. It clears both
bridge and adapter playback, steers GPT-Live to listen, and keeps caller audio
flowing. Playback resumes after caller quiet (240 ms) and provider output quiet
(200 ms). This is not echo cancellation and can react to sustained noise or
acknowledgments. Both flags default off and do not modify native GoLive calls.

The isolated runtime enables both flags following the
[call 05 investigation](../telephony/2026-10-31-speko-responsiveness.md).
They address setup waiting and local playback control; they do not establish
sub-1200 ms handset latency or fix the separate PBX hangup propagation issue.


## Backend response budget and farewell termination

`SPEKO_RESPONSES_REASONING` defaults to `none`. The 256-token backend response
cap includes hidden reasoning; the provider default exhausted that cap in the
[call 06 replay](../telephony/2026-10-31-speko-call06.md). Incomplete answers
remain errors. `provider_default` omits the field for rollback; explicit
`minimal`, `low`, `medium`, `high`, and `xhigh` are also accepted. This setting
controls the delegated text model, not the GPT-Live voice model. Backend logs
include duration and success without response text.

`SPEKO_AUTO_HANGUP=1` opts the SIP bridge into a narrow reciprocal-goodbye
policy. Both caller and assistant must say an allowlisted short farewell;
additional caller speech cancels it. After transcript settling and drained
voiced audio, the bridge sends normal close `1000 / assistant_hangup`, which
the SIP adapter turns into BYE. The default is off. This is not a general
semantic hangup tool and does not fix carrier-to-adapter BYE propagation.
