#timeout 100
set -eu
python3 - <<'CHECK'
from pathlib import Path
import hashlib
p=Path('/home/chuanming/golive-sip-ab/native-real-fb11e2c/call-01.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='d688c8117676998e5badd8ce1b2c4f3aa6775d91ce71087325be1bf66ed98f60'
exec(compile(p.read_bytes(),str(p),'exec'))
CHECK
