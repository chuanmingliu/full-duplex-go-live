#timeout 600
#push golive-ready-ack-151b2e3-linux /tmp/golive-ready-ack-151b2e3-linux
#push golive-ready-ack-f8e4d83-linux /tmp/golive-ready-ack-f8e4d83-linux
#push run-silent-ack-f8e4d83.py /tmp/run-silent-ack-f8e4d83.py
set -eu
python3 - <<'PYJOB'
from pathlib import Path
import hashlib
p=Path('/tmp/golive-ready-ack-151b2e3-linux')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='bc63c389516cb5f35fec41617973a7340fecc5e609c42ecf598bd7460bfc1910'
p.chmod(0o700)
p=Path('/tmp/golive-ready-ack-f8e4d83-linux')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='26fb388e620984e8ed82e9b2b55cbbfe28d5dc79843187d95acece0e7e5dd6a3'
p.chmod(0o700)
p=Path('/tmp/run-silent-ack-f8e4d83.py')
assert hashlib.sha256(p.read_bytes()).hexdigest()=='4cf6846108c8c1661ec4cef54184cc012574b49f28fe5378fc618571b42ea3dc'
exec(compile(p.read_bytes(),str(p),'exec'))
PYJOB
