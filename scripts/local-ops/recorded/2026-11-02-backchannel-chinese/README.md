# Chinese backchannel test operations, 2026-11-02

Historical exact Mac-runner jobs. Do not rerun completed operation names.
The test launches an isolated copy of the original `f1fd9a6` engine on 18765,
uses its original configuration/voice to synthesize public fixture sentences,
and shuts it down before evaluation. Nothing dials a real recipient.

The disposable evaluation container shares the ThinkPad host network to reach
the existing detector at 18087. It runs under the owner's UID with capability
removal and resource limits. It writes private results in
`~/golive-telephony/codex-latency/backchannel-zh-20261102`; recordings are mounted
read-only. The production engine, saved call context and Aria binding are
hash/state checked before and after. The detector is not restarted.

The two correlation jobs retain the original receipt and the expanded receipt
covering the one legacy-kept acknowledgment. All exported results contain only
numeric data, synthetic case identifiers and opaque call IDs. Credentials,
audio, customer text and private per-frame traces are excluded from Git.

See `manifest.json` for exact executed source hashes and
[the results](../../../../docs/telephony/2026-11-02-backchannel-chinese.md).
