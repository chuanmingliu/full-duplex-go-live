#timeout 90
#push ready-ack-notes-20261101.json /tmp/ready-ack-notes-20261101.json
#push publish-ready-ack-notes.py /tmp/publish-ready-ack-notes.py
set -eu
python3 - <<'PYJOB'
from pathlib import Path
import hashlib
p=Path('/tmp/ready-ack-notes-20261101.json')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='eb61415bf0259ef8e4572ed110350db48b141a8728a2e406acf55ca823aca39d'
p=Path('/tmp/publish-ready-ack-notes.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='fab8701216b7c9bd772c54cca61b627dd903ea3d24a4e69aee402317b41f2e2e'
exec(compile(p.read_bytes(),str(p),'exec'))
PYJOB
