# Complete tier comparisons, routing follow-ups, and detector recovery

The latest Active Call review exposed a model-generated comparison containing
100/150/200/300/400万 but omitting 50万. The catalog, cache and individual-premium
readout already supported that tier. A separate detector stream retired with
`stale_queue`; several natural quotation and coverage follow-ups selected no skill.

## Comparison contract

The engine adds an optional `compare_coverage` argument to its `quote_total`
tool definition. For example:

```json
{"compare_coverage":"三者险"}
```

This requests every configured tier of one coverage. The engine resolves its
alias and the effective `memory.quote_plan`, expands the catalog's ordered
options into `configs`, and removes the shorthand before dispatch to the
existing HTTP or MCP executor. For the current full catalog, those options are
50/100/150/200/300/400万. Prices still come from the existing quotation backend;
the expansion does not invent prices or change the confirmed selection.

`config` and `configs` cannot accompany the shorthand. Unknown coverage,
unavailable tier metadata and more than 16 options produce an explicit error,
not a default quote. Explicit subsets keep their existing `configs` behavior.
The original saved prompt and backend request protocol are unchanged. The
additional tool instruction and quotation skill tell the model when to request
all tiers; selection of this intent remains a model decision.

The shorthand is an Active Call engine feature, not a new standalone MCP tool
parameter. Direct MCP callers continue to use `configs`. The skill checks whether
the advertised tool supports the shorthand before recommending it.

## Routing

The default quotation routing now covers renewal-price questions and “按照…算”
changes. Concepts routing covers “就两百万吗” and adjustment questions mentioning
medical coverage. WeChat routing recognizes notification/account distinctions,
“加V”, and short progress reports, but retains the existing consent state guards.
A progress report is not an authenticated `addV` event and cannot set confirmed
addition status. Explanation-only exclusions remain in force.

These are seed/catalog content changes. An existing operator-owned MCP catalog
is not silently overwritten on server restart. Publish the intended skill fields
through the panel/API, preserving unrelated skills, rendering mode and edits.

## Detector fallback

Previously any queued chunk older than 120 ms permanently disabled the helper
for that call. The client now allows up to 500 ms of contiguous history to catch
up. It preserves every frame and sequence number; it never skips audio to make
a stale stream appear current. Predictions older than 150 ms are not published.
The existing input-lag, generation, utterance and speaker checks remain intact.

During catch-up, the existing legacy policy handles interruptions. A queue older
than 500 ms, overflow, audio gap, malformed reply, connection failure or socket
timeout still disables the helper. Socket deadlines remain 120 ms and input
pushes never block on inference. Acknowledgments, hesitation, unknown speakers,
echo and background speech retain their existing guards.

The voice replay additionally exposed false `audio_gap` fallback. The engine
previously truncated each packet’s fractional milliseconds before advancing its
input clock. It now carries remaining sub-millisecond pipeline samples between
packets, keeping the detector and VAD on the same cumulative clock. A regression
feeds 120 fragments of 17 samples and verifies 127 ms plus 8 remaining samples,
with the detector still available. Genuine lost audio and mid-call mute still
retire the helper.

## Validation and rollback

Results are recorded in the adjacent evidence directory. Recorded simulations
use isolated engine/CRM/MCP services, the original Active Call simulator path,
saved prompt/greeting/voice, preset openers and unchanged pricing delay. They
are not physical handset measurements. Quotation calculation, acknowledgment
and monetary readout are reported separately from normal responses.

Production configuration and services remain in place. The isolated harness
retains original profile, saved call and config snapshots; simulator-slot reuse
restores all original run files and verifies their hashes. A later deployment
must save the running engine and catalog revision, then publish only the intended
skill changes. Restore that engine and those skill fields to roll back; no CRM
migration or pricing-policy change is required by this fix.

The real-model checks passed all 20 option/explanation cases and three additional
comparison cases: all six tiers (including 50万), an exact two-tier subset, and an
options-only question that invokes no quotation. The 8,498-case HTTP combination
simulation passed with race detection. It checks premium sums against totals,
cache hits/misses/repeats, and call/fact isolation. Its zero-delay mock is a
functional test; it is not included in conversational latency figures.

## Recorded outcomes

Baseline `496945cf-72de-48b4-ac4a-a2b64a6da22e` ran build `3970624`.
The final audio-clock replay `5dfdde49-4692-44f7-ab2e-fa913bb85d31` ran
`coverage-repair-b`. Both used the saved original prompt. The intermediate
replay is retained as evidence, including its invalid link and truncated readout;
it is not presented as a successful final verification.

| Observation | Baseline | Final replay |
| --- | ---: | ---: |
| Normal content samples, excluding quotation origins | 10 | 8 |
| Normal content p50 | 1047.5 ms | 964 ms |
| Normal content p95 | 1137 ms | 1078 ms |
| Detector processed chunks | 271 | 4293 |
| Detector fallback | `audio_gap` | none |
| Missing/unverified final response records | 0 / 0 | 0 / 0 |
| Invalid tool links | 0 | 0 |

These are descriptive results from different conversational paths, not a
controlled speedup. The final scenario includes explicit overlap actions and
is not identical to the baseline scenario. Neither arm has 20 normal responses
or acoustic handset evidence. The strict 1200 ms handset goal is not certified.

The baseline replay also happened to include all six tiers; the omission was
observed in the preceding real call. The new shorthand makes expansion
deterministic once the model chooses the full-range intent.

The final transcript contains all six individual liability premiums and totals:
50万: 808 / 3973元; 100万: 1093 / 4258元; 150万: 1235 / 4400元;
200万: 1378 / 4543元; 300万: 1615 / 4780元; 400万: 1805 / 4970元.
These use the existing rounded mock-price speech convention. It also lists all
11 third-party medical options and all six driver medical options. The selected
comparison uses `compare_coverage`; the confirmed selection remains unchanged.

The cached comparison task took 12 ms. Its first monetary readout occurred
7442 ms after the primary caller request and 2139 ms after a coalesced follow-up.
The task was waiting for the conversational handoff; these intervals must not
be described as calculation time. The 2500 ms plus up to 2000 ms calculation
policy remains unchanged for actual pricing jobs.

The receiver recorded 1323 ms of overlap. The explicit interruption action
started but reached its reservation timeout during the initial exchange. No
qualifying normal-response server cutoff sample was observed. The separate
paced interruption test passed on both versions: caller onset to cut was
314 ms baseline and 310 ms candidate; echo, background and acknowledgment
cases did not cut. A 4 ms difference in this fixture is not evidence of a real
latency gain. The useful measured change is detector availability throughout
the final call, plus preserved fallback and interruption guards.

## First-time WeChat state

The final voice replay revealed that explicit consent was not recorded when
`memory.wechat` was absent. A further scoped prompt fix supplies the existing
unknown-state policy only on a WeChat-related turn with MCP enabled. It tells
the model to record explicit consent using `update_memory` before guidance.
Unrelated turns and already-known states keep their behavior; only a trusted
backend `addV` confirms successful addition. This last fix was verified in the
real-model harness, separately from the earlier voice replay: first consent
was recorded and the follow-up loaded the guarded WeChat skill. All five final
comparison/consent model cases passed.

Cleanup verified the production executable, profile, saved call, six CRM configurations and the three business skills against their snapshots. All 128 original simulator runs retained their hashes. The isolated engine, CRM, pricing, MCP fixture and Aria were stopped; recorded archives remain accessible at `http://192.168.71.9:18871`. Production stays on `3970624`; this change has not been deployed to the live route.
