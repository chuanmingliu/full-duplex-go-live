#timeout 100
#push call-native-interaction-7fc4237.py /tmp/call-native-interaction-7fc4237.py
set -eu
python3 - <<'PYJOB'
from pathlib import Path
import hashlib,subprocess,sys
p=Path('/tmp/call-native-interaction-7fc4237.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='066aa0082872ef674b3623016976c85ed499e03f304fe409a232c0b93738c473'
subprocess.run([sys.executable,str(p)],check=True)
PYJOB
