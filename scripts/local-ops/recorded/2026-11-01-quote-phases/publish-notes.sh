#timeout 90
#push quote-phase-notes-20261101.json /tmp/quote-phase-notes-20261101.json
#push publish-quote-phase-notes.py /tmp/publish-quote-phase-notes.py
set -eu
python3 - <<'PYJOB'
from pathlib import Path
import hashlib
p=Path('/tmp/quote-phase-notes-20261101.json')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='25b72c66fcc4915d5bb0702f074ed75ab2d497170e8fdcc8610cfd27aa9268ef'
p=Path('/tmp/publish-quote-phase-notes.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='1ae2bd4226e2bb9720d54394e45ed62f0cd87a9f3c26bc4bd110e809245d3a39'
exec(compile(p.read_bytes(),str(p),'exec'))
PYJOB
