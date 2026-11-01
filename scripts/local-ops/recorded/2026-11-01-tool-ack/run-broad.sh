#timeout 1000
set -eu
python3 - <<'CHECK'
import hashlib
from pathlib import Path
p=Path('/tmp/run-tool-ack-ab.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='2a25c4f39ce98062f6f7106052f41bc547d1d89493420f96dfb4d3ba7df9bea9'
exec(compile(p.read_bytes(),str(p),'exec'))
CHECK
