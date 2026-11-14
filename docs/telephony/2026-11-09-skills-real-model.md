# Real-model skills simulation, 9 November 2026

This report preserves the initial sequential-loading baseline and its failures. The current harness has since gained routing and state-handling fixes, so reproducing these exact historical results requires the original test snapshot.

See the [routing and recovery follow-up](2026-11-09-skills-fixes.md) for the implemented fixes and subsequent measurements.

## Outcome

The simulations exercised the configured `deepseek-chat` model with synthetic Chinese prompts and the deployed skills MCP service. Expected quotation and WeChat skills were selected in all 20 measured trials for each workflow. Ordinary questions caused one unnecessary quotation-skill load in 20 enabled trials. The complete suite has unresolved semantic failures around interrupted tasks and terminal WeChat state.

The production engine remained `9e29eed`, with its original profile and saved call configuration. Tests used isolated engine instances based on `6a32731`. Original mode disabled skills. Enabled mode used the local MCP endpoint. Both modes shared the same synthetic prompt, confirmed synthetic memory, model, temperature 0, 256-token output limit and mock TTS. Quotation calls went to a synthetic executor with a three-second delay. The original live services were preserved.

## Measurements

Two counterbalanced batches produced 120 benchmark trials, with 20 observations per scenario and mode. The table uses the median and nearest-rank p95. Numbers are milliseconds from synthetic text submission. First audio is the engine's mock-TTS output event. Quotation submission is entry into the native synthetic executor.

| Measurement | Original p50 / p95 | Skills p50 / p95 | Median increase |
| --- | --- | --- | --- |
| Ordinary answer, first audio | 774 / 1297 | 873 / 1143 | 99 |
| WeChat guidance, first audio | 736 / 1051 | 1671 / 2095 | 936 |
| Quotation acknowledgment, first audio | 1676 / 2569 | 2778 / 3738 | 1102 |
| Quotation submitted to executor | 1110 / 1217 | 2007 / 2676 | 897 |

Every measured quotation first-audio event preceded its synthetic calculation deadline. The acknowledgment figures therefore exclude price-result readout. The separate calculation-completion checks measured 3000.497 and 3001.155 ms in original mode, and 3000.818 and 3000.113 ms with skills. Both modes delivered a result and preserved the unchanged coverage items during a subsequent revision in the same session.

Sequential skill loading adds a model tool-selection step and another model completion. Its real-model cost is substantially larger than the earlier fixed-120-ms-provider test. Normal-response distributions overlap in this small sample. The normal-response tail estimate needs a larger sample to distinguish sampling variation from a consistent latency change.

ASR, real TTS, Active Call/RustPBX transport and acoustic handset playout are outside this test. The 1200 ms handset target requires speech and transport measurements. Both modes use a synthetic business prompt, so this is a comparison of the original and skills-enabled engine flows under controlled inputs.

## Skill selection and fallback

- Quotation revision loaded the expected skill in 20/20 enabled benchmark trials. Every native request preserved the confirmed compulsory and vehicle-damage coverage while requesting third-party coverage of 300万.
- WeChat help loaded the expected skill in 20/20 enabled benchmark trials.
- One ordinary question about insurance start time incorrectly selected `quote.requested`. The model loaded quotation instructions and eventually answered the ordinary question. The corresponding action trace contained the skill lookup followed by the ordinary answer.
- Simulated discovery and retrieval outages both produced a response in two runs per fault. Retrieval failure reached the model as `unavailable`. Conversation availability passed these checks.
- Fallback and no-match responses still had instruction-quality problems. One fallback response directed the caller to the Contacts tab despite the synthetic prompt's chat-list/search instructions. The confirmatory declined and unknown-consent cases invited the caller to open WeChat after the service correctly returned `no_match`.

The service's deterministic state conditions control which skill body is returned. Model adherence to business state requires separate response validation.

## Mock backend `addV`

The test adapter maps a backend trace named `addV` into the current engine's working memory.

```json
{
  "wechat": {"status": "added"},
  "last_backend_event": {
    "name": "addV",
    "source": "synthetic-backend"
  }
}
```

The test uses the existing `SeedMemory` queue to apply this update and waits for the engine-loop barrier before releasing a deliberately delayed resource response. The mock is scoped to one engine instance. A production event consumer and the upstream authenticated event envelope remain to be integrated.

| Injection point | State and retrieval result | Observed model behavior |
| --- | --- | --- |
| Between WeChat turns, enabled mode | `added` retained, old guidance expired, fresh lookup returned `no_match` | Both measured runs acknowledged success and stopped add-account guidance. Both still performed an unnecessary lookup. |
| Between WeChat turns, original mode | `added` retained | Both measured runs continued the earlier notification-search instructions. The preliminary pilot had acknowledged success, showing variability. |
| While a WeChat resource read was pending | Memory version changed and old guidance was rendered as `expired` in both runs | One run acknowledged successful addition. The confirmatory run continued notification-search instructions despite the updated memory. |

State propagation and stale-resource invalidation passed. Spoken handling of terminal state remains unreliable. The stored state alone is insufficient to guarantee that the next sentence abandons the completed workflow.

## Interruption results

The harness calls the engine's public `Interrupt` method. It checks emitted audio for 400 ms before submitting a new question. The assertion requires no old audio after 100 ms.

- Delayed retrieval interrupted successfully in both runs. Its late result did not resume the abandoned generation.
- Playback interruption passed in both original and enabled modes. The confirmatory run recorded no output frames after the interrupt timestamp in either mode. This is an engine event boundary with mock audio.
- Every recovery turn produced a response.
- After retrieval interruption, the new model response reloaded the abandoned WeChat skill and answered the old task before the new insurance-start-time question in both runs. The confirmatory assertion `recovery_has_no_old_skill` failed.
- After playback interruption, both original and enabled modes answered the new question and then continued the old claims-process explanation. The audio cancellation assertion passed, while manual review of the synthetic text found continued old-task content.

Generation cancellation works at the engine boundary. The next model request still needs an explicit representation that the old task was abandoned. Historical user messages can otherwise cause the model to start that task again in a fresh generation.

## Verification and interpretation

The first real-model batch exited with a failure for incorrect skill selection on an ordinary question. The confirmatory batch passed its main selection/latency scenarios and exited with a failure for abandoned-skill recovery. These outcomes are retained as evidence.

The initial collector captured five original quotation trials after their first audio and native submission, before later model continuation. Those latency timestamps are included in the table. The confirmatory collector waits for a completed, non-tool model response. Complete-chain checks and terminal-state review use that batch. The preliminary one-pair pilot is excluded from benchmark percentiles.

Local race-enabled skills regressions, compilation, `go vet` and runner Python syntax checks passed. Network tests are opt-in and use only the explicitly selected synthetic fixture and provider connection settings. The model receives synthetic prompts and state plus published skill text.

## Follow-up work indicated by the tests

1. Represent cancellation and the newest user task explicitly in the next model context. Suppress revival of the interrupted task unless the caller returns to it.
2. Make terminal WeChat state and refusal effective in response planning, including fallback. Hide ineligible WeChat actions from discovery and validate the response plan against the latest state version.
3. Reduce unnecessary skill-selection completions through an evaluated intent/state gate or safe prefetch. Preserve the ability to load revised server instructions on later turns.
4. Connect the real `addV` event with authenticated session/call correlation and duplicate-event handling, then repeat event-race tests through that adapter.
5. Repeat the same scenarios with real ASR/TTS and Active Call transport after semantic acceptance passes.

## Reproduction and evidence

The [test harness](../../internal/duplex/skills_real_model_test.go), [fault and event tests](../../internal/duplex/skills_real_edges_test.go) and [runner helper](../../scripts/local-ops/skills-real-model-sim.py) contain the synthetic prompt, mock event mapping and assertions.

Build a Linux test executable and transfer it with the helper in a reviewed Mac runner job. Run the helper from a fresh private output directory on the box.

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c ./internal/duplex -o /tmp/duplex.test
```

```sh
python3 simulate.py "$PWD/duplex.test" 10
```

The helper allowlists the active provider's credential variables and endpoint/model settings. It reads no saved call or private prompt. It writes `report.json` and `edges.json` containing synthetic responses and timing data. The runner jobs were `skills-real-model-pilot-1`, `skills-real-model-full-1` and `skills-real-model-full-2`.

The [summary](evidence/2026-11-09-skills-real-model/summary.json), [first batch](evidence/2026-11-09-skills-real-model/batch-1.json), [confirmatory batch](evidence/2026-11-09-skills-real-model/batch-2.json), [first fault run](evidence/2026-11-09-skills-real-model/edges-1.json), [confirmatory fault run](evidence/2026-11-09-skills-real-model/edges-2.json) and [pilot](evidence/2026-11-09-skills-real-model/pilot.json) retain the measured outcomes and synthetic response text.
