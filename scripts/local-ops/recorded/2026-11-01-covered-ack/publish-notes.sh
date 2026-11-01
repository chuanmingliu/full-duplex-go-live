#timeout 60
#push covered-ack-notes-20261101.json /tmp/covered-ack-notes-20261101.json
#push publish-covered-ack-notes-20261101.py /tmp/publish-covered-ack-notes-20261101.py
set -eu
python3 - <<'PYJOB'
from pathlib import Path
import hashlib,subprocess,sys
assert hashlib.sha256(Path('/tmp/covered-ack-notes-20261101.json').read_bytes()).hexdigest()=='24e501717e34db20f0ce967ee77da7378f3315b02c3762a36069def0ced2762f'
assert hashlib.sha256(Path('/tmp/publish-covered-ack-notes-20261101.py').read_bytes()).hexdigest()=='a3a8c7c27029a4f7b3addbb9217f7b4fd4bd6c23b3265c6e5a090b7da74a8d2f'
subprocess.run([sys.executable,'/tmp/publish-covered-ack-notes-20261101.py'],check=True)
PYJOB
