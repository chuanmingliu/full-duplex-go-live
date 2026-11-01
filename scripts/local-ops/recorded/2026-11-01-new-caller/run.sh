#timeout 600
#push golive-new-caller-7fc4237-linux /tmp/golive-new-caller-7fc4237-linux
#push golive-crm-3201ec4-linux /tmp/golive-crm-3201ec4-linux
#push run-new-caller-7fc4237.py /tmp/run-new-caller-7fc4237.py
#push verify-new-caller-7fc4237.py /tmp/verify-new-caller-7fc4237.py
#push new-caller-call-latency.py /tmp/new-caller-call-latency.py
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
p=Path('/tmp/run-new-caller-7fc4237.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='9f069647c9e6e3f8cb620fb4e8a98fe8c4fb9c41700d3d05d2c8419f442e7857'
p=Path('/tmp/verify-new-caller-7fc4237.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='b392c1317ca21ca33fe4ef306a7365713c659420de022373906280a042c48ea1'
p=Path('/tmp/new-caller-call-latency.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='c5df3bf2a120d4bcc241f75a1e2c28b3205a903dd94b33516fa21e11ea549a50'
subprocess.run([sys.executable,'/tmp/run-new-caller-7fc4237.py'],check=True)
subprocess.run([sys.executable,'/tmp/verify-new-caller-7fc4237.py'],check=True)
PYJOB
