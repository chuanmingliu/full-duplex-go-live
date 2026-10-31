# Mac-to-box operation runner

This branch maintains the local transport for reviewed golive operations.
The Bash runner watches a queue on the Mac, transfers declared artifacts and
executes each job body on the ThinkPad box through SSH. It is the path used for
call operations, evidence collection and deployment; it is not a phone service
or a CI server.

**Scope:** `codex/service-local-runner` in `full-duplex-go-live`.
See the [runner runbook](scripts/local-ops/README.md) and
[source provenance](scripts/local-ops/provenance.json).

## Owned code

| Path | Responsibility |
| --- | --- |
| `scripts/local-ops/runner.sh` | Serial queue consumer, SSH transfer/execution and receipts |
| `scripts/local-ops/enqueue.py` | Checks reviewed bytes, heartbeat, queue and unique job identity before atomic enqueue |
| `scripts/local-ops/stage-job.py` | Prepares a hash-verified body and small wrapper for larger jobs |
| `scripts/local-ops/recorded/` | Historical operation bodies and their manifests; not reusable deployment commands |

## Dependencies and startup

The Mac needs Bash, SSH/SCP and Python 3. The target needs Bash and the tools
required by the particular job; staged bodies also require Python 3. SSH access
and the remote staging directory must already work.

Use the installed runner if one is active. For a new installation, start exactly
one consumer in a Mac terminal:

```sh
export BOX='user@box'   # replace with the configured SSH destination or alias
export GOLIVE_RUNNER_HOME="$HOME/golive"
bash scripts/local-ops/runner.sh
```

The default control socket is `~/.ssh/golive-runner.sock`; override it with
`GOLIVE_RUNNER_SOCKET` when needed. Ctrl-C stops the foreground runner. Before
restarting or switching implementations, inspect the current job and remote
state; do not start competing consumers.

## Queue and evidence

State stays under `$GOLIVE_RUNNER_HOME/.runner`:

- `alive`: heartbeat; `enqueue.py` requires it to be less than 60 seconds old.
- `in/*.job`: queued operations with unique names.
- `out/<id>.part`, then `out/<id>.txt`: live and completed output.
- `done/*.job`: completed input bodies.

Write and review a job outside Git, then submit its exact SHA-256:

```sh
python3 scripts/local-ops/enqueue.py /private/tmp/unique-operation.job REVIEWED_SHA256
```

For a larger body, use `stage-job.py` as described in the runbook. Staging does
not copy or enqueue files. Runner `#push`, `#pull` and `#timeout` directives are
interpreted by the runner, not ordinary Bash.

A timeout kills the local SSH process, not necessarily the remote operation.
Transfer failures are recorded but do not automatically suppress the body.
Inspect receipts, artifact hashes and actual remote state before retrying;
never blindly repeat a call or deployment. Preserve private output and receipts
outside Git; they can contain call data or credentials emitted by a job.

## Checks and maintenance

Syntax/help checks do not contact the box or submit work:

```sh
bash -n scripts/local-ops/runner.sh
python3 -B scripts/local-ops/enqueue.py --help
python3 -B scripts/local-ops/stage-job.py --help
```

Keep the installed runner and queue available for rollback. Updating this branch
does not overwrite `~/golive/runner.sh`. Follow
[CALL-OPERATIONS.md](CALL-OPERATIONS.md) for call lifecycle and
[HANDSET-LATENCY.md](HANDSET-LATENCY.md) for the separate acoustic measurement.
