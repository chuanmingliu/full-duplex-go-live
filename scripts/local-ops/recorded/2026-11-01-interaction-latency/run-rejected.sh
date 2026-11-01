#timeout 720
#push golive-new-caller-7fc4237-linux /tmp/golive-new-caller-7fc4237-linux
#push golive-crm-3201ec4-linux /tmp/golive-crm-3201ec4-linux
#push interaction-latency-overrides.json /tmp/interaction-latency-overrides.json
#push run-interaction-latency-7fc4237.py /tmp/run-interaction-latency-7fc4237.py
#push verify-interaction-latency-7fc4237.py /tmp/verify-interaction-latency-7fc4237.py
#push interaction-latency-call-latency.py /tmp/interaction-latency-call-latency.py
set -eu
python3 - <<'PYJOB'
from pathlib import Path
import hashlib,subprocess,sys
p=Path('/tmp/golive-new-caller-7fc4237-linux')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='7d0d1e61713e85b6f903bfaa2aff46eebd3e834c8c5660b7d4a6bc93396ac65d'
p.chmod(0o700)
p=Path('/tmp/golive-crm-3201ec4-linux')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='76f53c252240c94b9947094fbfe7f6533f46acc1da8edee6f3cc8d7023f8bd83'
p.chmod(0o700)
p=Path('/tmp/interaction-latency-overrides.json')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='618953be30352eaf6ad2f6739e2803b54dc9b6230d9e5cd607ed088a5a76d62c'
p=Path('/tmp/run-interaction-latency-7fc4237.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='c894df290fcea703e8f8d8d9767510cea1d868151e1571f8c65805fd52a2a777'
p=Path('/tmp/verify-interaction-latency-7fc4237.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='eabc2dcb302ed88d9124a1b26812a285063b2008edb3bef07958cf943fd54419'
p=Path('/tmp/interaction-latency-call-latency.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='c5df3bf2a120d4bcc241f75a1e2c28b3205a903dd94b33516fa21e11ea549a50'
subprocess.run([sys.executable,'/tmp/run-interaction-latency-7fc4237.py'],check=True)
subprocess.run([sys.executable,'/tmp/verify-interaction-latency-7fc4237.py'],check=True)
PYJOB
