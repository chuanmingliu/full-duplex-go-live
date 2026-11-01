#timeout 300
#push golive-covered-f12b6f5-linux /tmp/golive-covered-f12b6f5-linux
#push native-f12b6f5-ci.json /tmp/native-f12b6f5-ci.json
#push deploy-native-f12b6f5.py /tmp/deploy-native-f12b6f5.py
set -eu
python3 - <<'PYJOB'
from pathlib import Path
import hashlib,subprocess,sys
assert hashlib.sha256(Path('/tmp/golive-covered-f12b6f5-linux').read_bytes()).hexdigest()=='7d367f327be47ee6abfd29e0bca5bc9cecc940ebacf0130c3bda3cab2bc166a4'
assert hashlib.sha256(Path('/tmp/native-f12b6f5-ci.json').read_bytes()).hexdigest()=='b2e75075d6450d91a9aa5bffb1cd69595171c737cd200e297e62e4b46534bfda'
assert hashlib.sha256(Path('/tmp/deploy-native-f12b6f5.py').read_bytes()).hexdigest()=='44104d9c97be4185b0c61acc212a962b2edec77473011c69b4f87c3dbec00661'
subprocess.run([sys.executable,'/tmp/deploy-native-f12b6f5.py'],check=True)
PYJOB
