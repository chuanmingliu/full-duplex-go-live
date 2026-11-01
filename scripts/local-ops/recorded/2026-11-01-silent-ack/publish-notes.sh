#timeout 90
#push silent-ack-notes-20261101.json /tmp/silent-ack-notes-20261101.json
#push publish-silent-ack-notes.py /tmp/publish-silent-ack-notes.py
set -eu
python3 - <<'PYJOB'
from pathlib import Path
import hashlib
p=Path('/tmp/silent-ack-notes-20261101.json')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='2893ab678270c36bdefd135da1d197e56b485ff339038e42f54b914cd2db61b9'
p=Path('/tmp/publish-silent-ack-notes.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='443301baa5aea8ec8bda49304205ad9f7e251f8f272743708abdd455425523fd'
exec(compile(p.read_bytes(),str(p),'exec'))
PYJOB
