#timeout 600
#push golive-ready-ack-1e38bac-linux /tmp/golive-ready-ack-1e38bac-linux
#push golive-ready-ack-151b2e3-linux /tmp/golive-ready-ack-151b2e3-linux
#push run-ready-ack-151b2e3.py /tmp/run-ready-ack-151b2e3.py
set -eu
python3 - <<'PYRUN'
from pathlib import Path
import hashlib
p=Path('/tmp/golive-ready-ack-1e38bac-linux')
assert hashlib.sha256(p.read_bytes()).hexdigest() == '10b64706455576b8e93ec1c55f8fc71a27b1dc7030fc0de65d0f5520e9e4d9fb'
p.chmod(0o700)
p=Path('/tmp/golive-ready-ack-151b2e3-linux')
assert hashlib.sha256(p.read_bytes()).hexdigest() == 'bc63c389516cb5f35fec41617973a7340fecc5e609c42ecf598bd7460bfc1910'
p.chmod(0o700)
p=Path('/tmp/run-ready-ack-151b2e3.py')
assert hashlib.sha256(p.read_bytes()).hexdigest() == '5032848a41bd1cdf3835c56779de26909960c0f8965bbabbbd7ab7ba50f1c02a'
p=Path('/tmp/run-ready-ack-151b2e3.py')
exec(compile(p.read_bytes(), str(p), 'exec'))
PYRUN
