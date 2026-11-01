#timeout 600
#push run-receipt-ab-2891b52-v2.py /tmp/run-receipt-ab-2891b52-v2.py
#push verify-receipt-ab-2891b52-v2.py /tmp/verify-receipt-ab-2891b52-v2.py
#push receipt-ab-v2-overrides.json /tmp/receipt-ab-v2-overrides.json
set -eu
python3 - <<'PYJOB'
from pathlib import Path
import hashlib,subprocess,sys
assert hashlib.sha256(Path('/tmp/run-receipt-ab-2891b52-v2.py').read_bytes()).hexdigest()=='43fa71c6cdd85f038c6ccdd6366a23884b0269ca89b6b5e463da7891be0ab76b'
assert hashlib.sha256(Path('/tmp/verify-receipt-ab-2891b52-v2.py').read_bytes()).hexdigest()=='13348e90d1394d8031f61bb4b016cd710ee67b0668fcb1379b30d35fcb3ece26'
assert hashlib.sha256(Path('/tmp/receipt-ab-v2-overrides.json').read_bytes()).hexdigest()=='f3317dc344bc9662025812ca2d4bec04f4fac228f1013e339258a9332f934751'
assert hashlib.sha256(Path('/tmp/golive-receipt-2891b52-linux').read_bytes()).hexdigest()=='82152bb45d6ef50a57370e9dd8c6d26caf60db6c4be566015586dd8dd8fa7472'
assert hashlib.sha256(Path('/tmp/receipt-call-latency.py').read_bytes()).hexdigest()=='c5df3bf2a120d4bcc241f75a1e2c28b3205a903dd94b33516fa21e11ea549a50'
assert hashlib.sha256(Path('/tmp/golive-new-caller-7fc4237-linux').read_bytes()).hexdigest()=='7d0d1e61713e85b6f903bfaa2aff46eebd3e834c8c5660b7d4a6bc93396ac65d'
assert hashlib.sha256(Path('/tmp/golive-crm-3201ec4-linux').read_bytes()).hexdigest()=='76f53c252240c94b9947094fbfe7f6533f46acc1da8edee6f3cc8d7023f8bd83'
subprocess.run([sys.executable,'/tmp/run-receipt-ab-2891b52-v2.py'],check=True)
subprocess.run([sys.executable,'/tmp/verify-receipt-ab-2891b52-v2.py'],check=True)
PYJOB
