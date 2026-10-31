#timeout 400
set -eu
python3 - <<'CHECK'
from pathlib import Path
import hashlib
p=Path('/home/chuanming/golive-sip-ab/native-ab-20261031/run-v3.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='73fb2e997b96f727e8b597f5139471546eae0e2f1a7e144f310fb50aa720e3e0'
exec(compile(p.read_bytes(),str(p),'exec'))
CHECK
