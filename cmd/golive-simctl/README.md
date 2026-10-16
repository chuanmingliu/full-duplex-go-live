# Receiving-only suite and independent evaluator

Build with `go build ./cmd/golive-simctl`. The CLI has no dial command, shell hooks,
provider defaults, or voice-runtime credential fallback. Synthetic suite example:

```sh
go run ./cmd/golive-simctl suite \
  --file scenarios/suites/synthetic.json \
  --url http://127.0.0.1:18865 --token-env SIM_CONTROL_TOKEN --output ./suite-output
```

The example uses the unchanged cooperative preset and a suite-local busy fixture
with a 10-second hangup and 20-second idle timeout. This avoids the legacy busy
preset's tie between its 60-second hangup and maximum duration; that legacy
preset and its hash remain unchanged. The external caller must speak long enough
for the 500-ms interruption and remain connected until the scheduled hangup to
supply completed action evidence.

All scenarios are validated before any HTTP request. Paths are relative to the
manifest. Each scenario runs `repeat` times (1–10), sequentially; at most 100 runs.
Each fresh reservation produces one flushed JSONL record on stdout:

```json
{"type":"ready","run_id":"sim-example","scenario_id":"appointment-cooperative","trial":1,"called_sip_uri":"sip:sim-example@simulator.invalid"}
```

The independent caller consumes that record and originates the call to its exact
URI. The runner only waits, reads reports and writes private artifacts. It never
retries preparation; an ambiguous POST stops the suite. A known owned run is
stopped after cancellation/error with a separate two-second cleanup deadline.
No pre-existing run is stopped. Each request is bounded to 10 seconds and 1 MiB;
each run is bounded to eight minutes. Polls occur every 500 ms. Redirects are
rejected. Control URLs are origins with no credentials, query or fragment; HTTP
is permitted only for literal loopback addresses and `localhost`.

`summary.json` records invocation identity, scenario/prompt hashes, repetition,
run IDs, protocol/semantic verdicts, and counts. A sanitized `<run-id>-report.json`
is saved per terminal run. New directories use 0700; artifacts use 0600. Free-form
receiver evidence/reasons/provenance are omitted. Protocol checks explicitly
select lifecycle/action evidence and ignore legacy semantic placeholder checks.
Scenarios with expectations remain semantically indeterminate until evaluated;
scenarios without expectations record `not_requested`, never semantic pass.
A protocol pass alone cannot pass requested semantic expectations.

To evaluate, explicitly retain transcripts in the scenario before preparing the
run, obtain its terminal report and `/sim/v1/runs/<run-id>/transcript` envelope,
and configure the independent judge with these environment variables:

- `SIM_EVAL_BASE_URL`: HTTPS base ending exactly in `/v1`, e.g. `https://judge.example/v1`.
  The CLI appends `/chat/completions`. HTTP is permitted only on loopback.
- `SIM_EVAL_MODEL`: explicit independent judge model.
- `SIM_EVAL_API_KEY`: independent judge credential, read only by `evaluate`.

```sh
go run ./cmd/golive-simctl evaluate \
  --scenario scenario.json --report run-report.json \
  --transcript run-transcript.json --output run-evaluation.json
```

The evaluator verifies matching run/scenario/prompt identities, completed phase,
retention, no truncation or reported failure, two-sided normalized transcript,
monotonic timestamps and expectations before making a provider request. Report
`transcript_truncated` / `events_truncated` and `failures` array, transcript
`truncated`, and every
entry's `at_ms` must be explicitly present and non-null. Legacy artifacts missing
these observations remain indeterminate; omitted fields are never assumed false
or zero. Each
input is at most 1 MiB, with at most 256 turns and 8192 bytes per utterance.
Judge calls use a 30-second bound, 1 MiB response limit, no redirects or tools.
Task/facts/expectations are sent separately from role-labeled untrusted utterances.
Every exact expectation ID must appear once with valid 1-based evidence indices;
pass/fail must cite assistant evidence. Refusals, partial/malformed output and
missing evidence are indeterminate. Any valid fail dominates, then indeterminate.
Provider reasons are validated for size and replaced by fixed safe explanations;
raw completions and credentials are never saved. The separate evaluation includes
a SHA-256 hash of the canonical JSON transcript envelope and prompt version.
The raw run report is never overwritten. LLM judgments are fallible and remain
separate from deterministic protocol evidence.

Exit codes: **0** every requested check passed; **1** failed or indeterminate
verdict (including missing/invalid input artifacts, evidence, configuration or
judge rejection during evaluation); **2** usage, suite input/operation, or
persistence error. Invalid evaluator inputs produce a separate indeterminate
artifact with empty untrusted identity fields and no provider request. Diagnostics
never include HTTP response bodies or transport errors containing credentials.
