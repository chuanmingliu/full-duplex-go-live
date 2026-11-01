# Native SIP engine deployment: f12b6f5

On 2026-11-01 at 14:35:13 (Asia/Shanghai), the existing Mac runner upgraded only
the idle native SIP test engine at `127.0.0.1:9895`, under
`/home/chuanming/golive-sip-ab/native-real-fb11e2c`, from `9547d22` to `f12b6f5`.
The SIP adapter remains `fb11e2c`. No call was placed.

Both jobs in [CI run 41280473376](https://github.com/chuanmingliu/full-duplex-go-live/actions/runs/41280473376)
passed for the exact source commit. This includes the full Go race suite, vet,
public latency regressions, repeated/sustained adapter simulations and fuzzing.
The binary was built from a clean archive of that commit and hash-checked before
installation. Runtime readiness and non-dialing adapter preflight passed.

`deploy.py` is a single-use, idle-only operation with a deployment lock,
configuration and binary hash guards, an unchanged-service check and automatic
rollback on failed readiness/preflight. It modifies only the native engine
executable and restarts only its Compose service. It preserves the previous
binary at:

```
/home/chuanming/golive-sip-ab/native-real-fb11e2c/releases/engine-f12b6f5-20261101/golive-9547d22
```

Before any rollback, verify the native pool is idle. Restore that saved binary
atomically through the existing runner, restart only the native `engine`
Compose service, and verify `/healthz` returns `9547d22`. Never replay this
completed deployment job or restart an active call.

Compose, provider/SIP configuration, saved call template, native Journal process,
original engine on 8765, other experiment engines, and Active Call remain
unchanged. Recording, archives and the 365-day retention settings are preserved.
The newer caller-input measurement repair `302267b` was tested in an isolated
simulator and is **not** included in this deployment; its full CI is pending.
No handset latency qualification is claimed by deployment readiness.

`deploy.sh` is the exact runner body. Job SHA-256:
`86edcd0f4b9b0b1279a78741ec3cb86bd87a26434d95579c7680270edfc5d0b3`.
`ci.json` and `receipt.json` retain the verified source and runtime evidence.
