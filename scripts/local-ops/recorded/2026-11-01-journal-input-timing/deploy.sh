#timeout 120
#push golive-journal-302267b-linux /tmp/golive-journal-302267b-linux
#push deploy-journal-input-timing-v2.py /tmp/deploy-journal-input-timing-v2.py
set -eu
python3 - <<'PYJOB'
from pathlib import Path
import hashlib,subprocess,sys
assert hashlib.sha256(Path('/tmp/golive-journal-302267b-linux').read_bytes()).hexdigest()=='ca3e4d8adfa306c6e3533668f8c6fd993c2826ba93aa7703153f7c81883a02b4'
assert hashlib.sha256(Path('/tmp/deploy-journal-input-timing-v2.py').read_bytes()).hexdigest()=='e5b1bcea396100e0abdb272c4325d618de600fe8d54ab10d2e26788e07bf0ca5'
subprocess.run([sys.executable,'/tmp/deploy-journal-input-timing-v2.py'],check=True)
PYJOB
