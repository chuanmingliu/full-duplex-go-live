#timeout 400
set -eu
python3 - <<'CHECK'
from pathlib import Path
import hashlib
p=Path('/home/chuanming/golive-sip-ab/native-ab-20261031/run-v4.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='a7da241c96ab05e2c8dc05b4223047897d1427c635f86123f575528cdb55a302'
exec(compile(p.read_bytes(),str(p),'exec'))
CHECK
