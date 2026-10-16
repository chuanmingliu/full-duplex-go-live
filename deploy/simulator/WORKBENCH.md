# Simulator workbench and regression testing

The assistant service originates the call; this service answers as a synthetic customer. Both sides can use the same continuously listening ASR → LLM → TTS engine. Their call-control roles and instructions differ. Coding agents prepare scenarios and inspect results outside the audio conversation.

This extension follows the existing [golive configuration UI](https://github.com/chuanmingliu/full-duplex-go-live/blob/master/web/index.html) and the task-level testing approach described in [LiveKit Agent Simulations](https://livekit.com/blog/testing-voice-agents-with-agent-simulations). LiveKit is a design reference, not a dependency. The receiver does not gain an outbound dialing endpoint.

## Configure an inbound run

Start the isolated simulator using the [package instructions](README.md), then open `http://127.0.0.1:18865/sim/`. Enter this simulator's `SIM_CONTROL_TOKEN`. The browser keeps it in memory until disconnect or page close; it does not save it in browser storage.

The workbench separates these settings:

- **Scenario:** persona, task goal, immutable synthetic facts, greeting, language, behavioral instructions and expected assistant behavior. Goals and behavior guide the customer. Evaluation expectations are kept out of the live customer's prompt.
- **Call behavior:** answer delay, maximum duration, idle timeout, turn limit and scheduled speech/hangup actions. Event-triggered interruption is controlled test behavior; it is not an adaptive backchannel model.
- **Voice profile:** the effective running provider choices and an editable next-start profile. Exporting a profile changes no running service. Apply it only when deliberately starting this isolated simulator with that profile and dedicated provider credentials.
- **Runs and results:** prepare a reservation, copy its exact called SIP target, inspect state/events, stop an explicit run, and download terminal evidence.

The assistant-side caller must use `sip:<run_id>@simulator.invalid` (or the configured domain) from the prepared run. A fixed public telephone number still needs a verified route that preserves this identity. An HTTP-ready process does not prove that the gateway, cellular channel or inbound route is ready.

Import/export scenario JSON to version a test case. Keep the exact exported scenario used to prepare the run: the independent evaluator checks its hashes against the report and transcript. Transcript retention must be enabled before the run; it cannot be recovered afterward if disabled. Audio recording remains unsupported.

The receiving audio session uses fixed 16 kHz PCM. The panel displays that effective rate and includes it in exported profiles. Supported scheduled actions are speech and hangup; scripted silence is not implemented by this receiver.

## Run a sequential suite

Build the controller CLI:

```sh
go build -o /tmp/golive-simctl ./cmd/golive-simctl
/tmp/golive-simctl suite \
  --file scenarios/suites/synthetic.json \
  --url http://127.0.0.1:18865 \
  --token-env SIM_CONTROL_TOKEN \
  --output /tmp/simulator-suite
```

A manifest names scenario paths relative to itself and a bounded repeat count. The CLI prepares one reservation, emits a JSON-lines `ready` record containing its run ID and exact SIP target, and waits for the external assistant to call. It retrieves the terminal report before preparing the next trial. It never dials, invokes a shell hook, or retries a call automatically. Cancelling the suite cleans up only the run that invocation created.

Repeated trials expose variability; repeating the same scenario does not make model output deterministic. Protocol results and task results are separate. An audio transport success is not a task pass, and an unevaluated task remains indeterminate.

Exit status is 0 when every requested check passes, 1 for failed/indeterminate verdicts, and 2 for usage, input, operation or persistence errors. Scenarios without semantic expectations record `not_requested`, not a semantic pass. See the CLI reference for exact bounds and output schemas.

## Evaluate a completed conversation

Download the report and, when enabled, the transcript envelope. Use the original scenario JSON. Configure a separate judge explicitly:

```sh
# Set these in the invoking process; do not put keys in a scenario or profile.
export SIM_EVAL_BASE_URL=https://your-judge-provider.example/v1
export SIM_EVAL_MODEL=your-judge-model
# Set SIM_EVAL_API_KEY through your normal secret-management workflow.
/tmp/golive-simctl evaluate \
  --scenario appointment-correction.json \
  --report report.json \
  --transcript transcript.json \
  --output evaluation.json
```

Invoking evaluation sends the retained, redacted conversation and synthetic task facts to that configured judge. The voice service's provider credentials are not reused implicitly. Evaluation runs after the call and cannot answer, interrupt, or steer it.

The evaluator verifies run/scenario/prompt identity, retention and transcript completeness before requesting grades. The judge grades the remote assistant against the scenario's expectations and supplies turn references. Invalid or missing evidence stays indeterminate. Judge results are saved separately from the original transport report; the model's verdict is fallible and can be reviewed against the cited turns. Free-form judge explanations are replaced with fixed descriptions to keep conversation material out of those explanation fields. Evaluation does not rewrite the suite summary.

## Evidence boundaries

Use the default mock providers to test configuration, control flow and transport. Their synthetic tones do not validate recognizable speech. Use independently configured real speech providers for conversation acceptance, then a dedicated verified carrier route for telephone acceptance. The recorded SIP results in `validation-results.json` remain the original 2026-10-13 evidence, not results of this extension.

Timing is measured at the receiver's application audio boundary, not at a remote handset. Missing, failed or overlapping samples retain their invalid reasons. No real-provider or carrier tests are implied by the workbench, evaluator fixtures or CLI tests.
