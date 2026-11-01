#timeout 660
#push golive-new-caller-7fc4237-linux /tmp/golive-new-caller-7fc4237-linux
#push golive-crm-3201ec4-linux /tmp/golive-crm-3201ec4-linux
#push interaction-latency-v2-overrides.json /tmp/interaction-latency-v2-overrides.json
#push run-interaction-latency-7fc4237-v2.py /tmp/run-interaction-latency-7fc4237-v2.py
#push verify-interaction-latency-7fc4237-v2.py /tmp/verify-interaction-latency-7fc4237-v2.py
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
p=Path('/tmp/interaction-latency-v2-overrides.json')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='326b99fb2dd38bee9540165742d12ecb2290780c58c71e3d989d6739bae3f59d'
p=Path('/tmp/run-interaction-latency-7fc4237-v2.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='7e8dfd93330ec3ffb9787456465b03973f4f8d2e495620f52164284e4d8066b1'
p=Path('/tmp/verify-interaction-latency-7fc4237-v2.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='60c461b16ab5a3d6b0319a6360dfcfb9b2e41bde09b6d8dec315adb5c70f9b63'
p=Path('/tmp/interaction-latency-call-latency.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='c5df3bf2a120d4bcc241f75a1e2c28b3205a903dd94b33516fa21e11ea549a50'
subprocess.run([sys.executable,'/tmp/run-interaction-latency-7fc4237-v2.py'],check=True)
subprocess.run([sys.executable,'/tmp/verify-interaction-latency-7fc4237-v2.py'],check=True)
PYJOB
