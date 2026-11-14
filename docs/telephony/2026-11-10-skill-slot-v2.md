# Optional skill-content slot, 2026-11-10

## Implementation

Active Call's GoLive renderer now supports an optional v2 host template with a literal `{{ skill_content }}` slot. The original configured system prompt remains the prefix. Verified selected skill bodies follow in an `additional_skill_instructions` section. Source, digest, scope and memory version remain in the per-request guidance envelope, with the body included once.

The Skills MCP panel publishes `prompt_rendering.version` with the catalog. Version 1 is the default and uses the existing appended guidance. Version 2 takes effect on the next model request after publication by a compatible server and engine. A service failure, invalid rendering version, integrity error or stale snapshot uses the original-prompt fallback. Existing no-match requests keep their original system prompt.

The fixed template, message order, state handling and rollback behavior are documented in the [rendering guide](../architecture/09-skills-prompt-rendering.md#optional-v2-skill-content-slot). The panel configures the rendering version and published skill bodies.

## Comparison setup

- ThinkPad isolated catalog on loopback port 18795, using the candidate binaries and seed skills.
- Real `deepseek-chat` provider from the active engine's connection configuration.
- Saved default instructions from `~/golive-last-call.json`, supplied privately to both versions. SHA-256 `639dcecf24dddc73274a7412c017e36258b51679c2fd3ad4e72a595453aa8d25`.
- Synthetic session memory, consenting WeChat state, synthetic text requests and a three-second quotation executor.
- Ten trials per scenario and version, alternating v1/v2 order. Sixty comparative cases plus fallback and trusted `addV` cases, all passing.
- Temperature zero, 256 maximum output tokens, prefix warming disabled by the test harness and mock TTS. These are simulation settings.
- Measured boundary is text input to first mock audio. ASR, real TTS, Active Call transport, carrier and acoustic handset delays require media/call measurements.

The runner reported exit 0. Before/after hashes confirmed preservation of the active profile, running executable, launch arguments and saved default-call file. Detailed model output stays private on the box. The committed [aggregate evidence](evidence/2026-11-10-skill-slot-v2/summary.json) contains timings and assertion outcomes.

## Latency

All values are milliseconds. With ten observations, nearest-rank p95 is the largest observation in that group.

| Scenario | v1 first-audio p50 | v2 first-audio p50 | v1 first-audio p95 | v2 first-audio p95 |
| --- | ---: | ---: | ---: | ---: |
| Normal response | 792 | 767 | 1,160 | 1,081 |
| WeChat notification guidance | 647 | 624 | 1,067 | 928 |
| Quotation acknowledgment | 1,743 | 1,770 | 2,312 | 2,816 |

Normal and WeChat first audio were faster in this sample. Quotation acknowledgment's median rose 27 ms and its p95 rose 504 ms. The ordinary no-match request assembles the same prompt under both versions, so its timing variation illustrates provider and run-to-run variability. This sample supports functional optional-v2 use and leaves quotation tail latency as a follow-up measurement.

### Quotation timing

| Boundary | v1 p50 | v2 p50 | v1 p95 | v2 p95 |
| --- | ---: | ---: | ---: | ---: |
| Input to quotation submission | 1,084 | 982 | 1,276 | 2,985 |
| Quotation executor calculation | 3,001 | 3,001 | 3,001 | 3,001 |

Every quotation trial requested calculation, retained confirmed compulsory and vehicle-damage coverage, changed third-party liability to 300 万 and completed the synthetic calculation. Acknowledgment generation and tool submission can occur in different orders. Calculation runs asynchronously, so these boundaries should be read independently. Final spoken quotation-result latency was outside this comparison's measurement scope.

## Functional checks

The real-model suite verified the default prompt prefix, expected skill selection, expected slot placement, an audible mock response and a bounded completed model chain in all comparative cases. The v2 service-outage case used the original prompt and produced first audio at 1,047 ms. A trusted synthetic `addV` event suppressed the addition skill and produced a completion acknowledgment at 847 ms.

Deterministic race-enabled tests additionally cover literal template preservation, versions 1 and 2, invalid-version fallback, catalog-revision races, per-request mode refresh, quotation execution and interruption cancellation in both rendering versions. Catalog tests cover absent-field defaulting, persisted v2 publication, invalid-version rejection and returning to v1. A headless browser test exercised the actual panel's v1 default, v2 publication, reload persistence, WeChat preview with literal placeholders and v1 rollback.

These interruption tests verify cancellation and stale-output prevention. Acoustic interruption delay remains a separate call/media measurement.

## Reproduction and activation

Build the root module's `internal/duplex` tests as Linux `duplex.test`, and the `services/skills-mcp` module's tests as Linux `catalog.test`. Package them with `scripts/local-ops/skills-real-model-sim.py` renamed to `simulate.py`. Transfer with the reviewed Mac runner into a fresh private directory, then run the following on the box.

```sh
python3 simulate.py "$PWD/duplex.test" 10 slot fixture
```

The helper requires the isolated catalog for this phase and stops it on completion. It writes a private `report.json`. Keep prompt text, detailed conversation traces and provider credentials out of committed evidence.

This task validates the candidate through isolated simulation. Live activation requires deploying the paired engine and Skills MCP release, then choosing **Prompt rendering → Version 2** and publishing. The default remains version 1. An operator can publish version 1 to restore the previous rendering on the next request.
