#timeout 420
#push run-receipt-ab-2891b52-v4.py /tmp/run-receipt-ab-2891b52-v4.py
#push verify-receipt-ab-2891b52-v4.py /tmp/verify-receipt-ab-2891b52-v4.py
#push receipt-ab-v4-overrides.json /tmp/receipt-ab-v4-overrides.json
set -eu
python3 - <<'PYJOB'
from pathlib import Path
import hashlib,subprocess,sys
assert hashlib.sha256(Path('/tmp/run-receipt-ab-2891b52-v4.py').read_bytes()).hexdigest()=='d44db1fccde234fba265118dc000cea3545e88235dd007507ea9c21095f0b47c'
assert hashlib.sha256(Path('/tmp/verify-receipt-ab-2891b52-v4.py').read_bytes()).hexdigest()=='7fb8f562da3690b4b167ef28765b84fab5111d468aab8afcaa60a69d5cab6f3c'
assert hashlib.sha256(Path('/tmp/receipt-ab-v4-overrides.json').read_bytes()).hexdigest()=='40868f8146b5d7a3211aff022944107ce848200ac5f2038ce159d9c8070de3ee'
assert hashlib.sha256(Path('/tmp/golive-receipt-2891b52-linux').read_bytes()).hexdigest()=='82152bb45d6ef50a57370e9dd8c6d26caf60db6c4be566015586dd8dd8fa7472'
assert hashlib.sha256(Path('/tmp/receipt-call-latency.py').read_bytes()).hexdigest()=='c5df3bf2a120d4bcc241f75a1e2c28b3205a903dd94b33516fa21e11ea549a50'
assert hashlib.sha256(Path('/tmp/golive-crm-3201ec4-linux').read_bytes()).hexdigest()=='76f53c252240c94b9947094fbfe7f6533f46acc1da8edee6f3cc8d7023f8bd83'
subprocess.run([sys.executable,'/tmp/run-receipt-ab-2891b52-v4.py'],check=True)
subprocess.run([sys.executable,'/tmp/verify-receipt-ab-2891b52-v4.py'],check=True)
PYJOB
