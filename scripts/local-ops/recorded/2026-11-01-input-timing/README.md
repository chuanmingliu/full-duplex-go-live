# Caller-input timing SIP simulator validation

Historical single-use job for engine `302267b` and diagnostic CRM `3201ec4`.
The same 90-second quotation scenario, provider profile, saved prompt/greeting/
voice, and deliberately slow quote service are retained. The only engine
change is input-provenance instrumentation, with result delivery unchanged.

`run.py` refuses active real engines or simulator runs, occupied test ports,
an existing attempt or changed original configuration. The reserved SIP
address must be `sim-*`; no real number can be dialed by this operation. It
terminates its isolated processes and checks original config hashes and Active
Call container identity/start afterward. The existing Journal archive reader
is reused without restarting it.

`verify-original.py` was the original verifier named by `run.sh` and its manifest. It verifies all five archive file hashes, a closed session, transcript
speakers and both audio channels. It initially required every delivery to have a direct input origin and failed on two chained tasks after the call ended. The retained `verify.py` and `collect.sh` perform collection only, preserve those two links as unverified, and run the updated analyzer with
`--quote-sla separate --target-ms 1150 --min-turns 20 --metric both`. It retains
result and readout rows, missing/unverified evidence, and the strict sample
minimum. No timing is backfilled into old recordings. The existing running
Journal may still display older parser results until its verified upgrade;
this job's numeric report uses the new analyzer explicitly.

`run.sh` contains the exact runner job; `manifest.json` binds all five payloads.
Job SHA-256:
`d1a241d16cb3b6e6a4b83aa21f6b5f9f2d15f04b2d205f8b3dd70751e80fc9f2`.
No handset qualification or production promotion is implied.

The collection-only job exited 0 at 14:31:39; its analyzer gate still failed. See the retained retrospective for numeric results.
