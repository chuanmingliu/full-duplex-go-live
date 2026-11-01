# SIP/RTP test journals on RobotGo

Open [SIP/RTP tests](https://robotgo.liuchuanming.ai/#/experiments) in the existing
Call Journal. The tab selects a separate journal and lists its calls with audio controls and
download links. Each call opens its full conversation, timing evidence, filler
events and retrospective. Calls are rendered directly rather than embedded in
frames, which can fail to load through the protected public route. Original calls
remain in Overview and are not pooled with these experiments.

| Selection | Source on the box | Initial calls | Type |
| --- | --- | --- | --- |
| Real SIP/RTP calls | `192.168.71.9:9896` | 2 | Real handset calls, original GoLive |
| Tool / acknowledgment A/B | `192.168.71.9:9906` | 4 | Broad simulations |
| Quotation A/B | `192.168.71.9:9916` | 2 | Focused simulations |

The source list is `experiments.json` inside Journal's existing `-notes`
directory. [The checked-in list](../../journal/experiments.json) documents the
current box deployment. Entries contain a stable `id`, `title`, `description`,
`kind` (`real` or `simulated`) and fixed HTTP `url`. Changing this file is picked
up without restarting Journal. Missing configuration produces an empty list;
invalid configuration produces an explicit error. Only an operator can change
the source addresses; browser query parameters cannot select upstream hosts.

The parent Journal serves its own UI at `/experiments/<id>/` and proxies an
allowlist of read-only source API routes through that same origin. Audio range
requests, peaks, transcripts, reviews and call detail are supported. POST,
feedback writes, dialing, Talker probing and arbitrary upstream paths are not
available through the mount. Browser cookies, bearer tokens and Cloudflare
identity headers are not forwarded to source journals. Redirects and unavailable
sources fail closed. The original Journal API keeps its existing behavior.

Cloudflare Access continues to protect the existing hostname and all these
paths. No DNS, tunnel ingress, Access policy or new public listener is needed.
Journal has no built-in authentication; preserve the external access controls.
No audio or transcript is copied into Git. Source archives remain authoritative
and their processes must remain available. If a source is down, the selected
journal reports the error and the other selections remain usable.

The simulations are clearly labeled, including in the full-page view. Their
server or receiver measurements do not establish acoustic handset latency.
Failed/incomplete tests remain visible; read their retrospective before comparing
numbers. See [the A/B report](../telephony/2026-11-01-tool-ack-comparison.md).

## Release and rollback

Build only `cmd/journal` from the reviewed commit for Linux amd64. Save the
previous Journal executable, argument vector, PID-file location and source list
before replacing it. Restart only Journal with its existing arguments and working
directory. Verify the original overview, all three mounted overviews, call detail,
transcript and WAV range requests. Keep original GoLive, SIP adapters, CRM,
Active Call and recording/archive locations unchanged.

Rollback restores that saved executable and source list, then restarts Journal
with the saved argument vector. No call archive migration is involved.

Validation: Journal package race tests cover source isolation, audio ranges,
credential stripping, blocked writes, rejected redirects, unavailable upstreams,
invalid configuration, static assets and empty configuration. Verify the signed-in
public browser after deployment as well as the origin API; an unauthenticated
request to either the root or an experiment API should still reach Access login.
