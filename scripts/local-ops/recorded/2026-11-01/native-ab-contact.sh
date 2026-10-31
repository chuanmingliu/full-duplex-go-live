#timeout 240
set -eu
python3 - <<'CHECK'
from pathlib import Path
import hashlib
p=Path('/home/chuanming/golive-sip-ab/native-ab-20261031/run-v6.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='2540292456a57ce2333b4693b8080a0595675d80315161361b73a094faecd951'
exec(compile(p.read_bytes(),str(p),'exec'))
CHECK
