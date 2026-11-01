#timeout 90
#push quote-policy-notes-20261101.json /tmp/quote-policy-notes-20261101.json
#push publish-quote-policy.py /tmp/publish-quote-policy.py
set -eu
python3 - <<'PYJOB'
from pathlib import Path
import hashlib
p=Path('/tmp/quote-policy-notes-20261101.json')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='4b0a9f89f468e005e3b599cee1494b3eabdd56ed9e3402f7edbe68f0be5bf91c'
p=Path('/tmp/publish-quote-policy.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='c7a3bce737aab63cd96784f115e2b458864f533f30fc6141b498751032c40823'
exec(compile(p.read_bytes(),str(p),'exec'))
PYJOB
