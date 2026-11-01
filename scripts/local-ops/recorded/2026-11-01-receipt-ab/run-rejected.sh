#timeout 600
#push golive-receipt-2891b52-linux /tmp/golive-receipt-2891b52-linux
#push run-receipt-ab-2891b52.py /tmp/run-receipt-ab-2891b52.py
#push verify-receipt-ab-2891b52.py /tmp/verify-receipt-ab-2891b52.py
#push receipt-ab-overrides.json /tmp/receipt-ab-overrides.json
#push receipt-call-latency.py /tmp/receipt-call-latency.py
set -eu
python3 - <<'PYJOB'
from pathlib import Path
import hashlib,subprocess,sys
assert hashlib.sha256(Path('/tmp/golive-receipt-2891b52-linux').read_bytes()).hexdigest()=='82152bb45d6ef50a57370e9dd8c6d26caf60db6c4be566015586dd8dd8fa7472'
assert hashlib.sha256(Path('/tmp/run-receipt-ab-2891b52.py').read_bytes()).hexdigest()=='b65349bf94ea21989021133dd585cac7508b8cc7d11639abcccf1ddecb576bc6'
assert hashlib.sha256(Path('/tmp/verify-receipt-ab-2891b52.py').read_bytes()).hexdigest()=='dabe24d6691cc34266a086e538bddc44b8ea5607695349ebb8abeaf79b4366e1'
assert hashlib.sha256(Path('/tmp/receipt-ab-overrides.json').read_bytes()).hexdigest()=='469a91df6a399d25519bef6b802fab3cf0c0213cc056b6fdcf90af574bfb7d99'
assert hashlib.sha256(Path('/tmp/receipt-call-latency.py').read_bytes()).hexdigest()=='c5df3bf2a120d4bcc241f75a1e2c28b3205a903dd94b33516fa21e11ea549a50'
assert hashlib.sha256(Path('/tmp/golive-new-caller-7fc4237-linux').read_bytes()).hexdigest()=='7d0d1e61713e85b6f903bfaa2aff46eebd3e834c8c5660b7d4a6bc93396ac65d'
assert hashlib.sha256(Path('/tmp/golive-crm-3201ec4-linux').read_bytes()).hexdigest()=='76f53c252240c94b9947094fbfe7f6533f46acc1da8edee6f3cc8d7023f8bd83'
Path('/tmp/golive-receipt-2891b52-linux').chmod(0o700)
subprocess.run([sys.executable,'/tmp/run-receipt-ab-2891b52.py'],check=True)
subprocess.run([sys.executable,'/tmp/verify-receipt-ab-2891b52.py'],check=True)
PYJOB
