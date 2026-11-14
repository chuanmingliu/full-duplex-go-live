#timeout 1200
#pull /home/chuanming/golive-telephony/codex-latency/deployment-9547d22-opener-release-20261031-5.json codex-deployment-9547d22.json
set -eu
cd "$HOME/golive-telephony"
python3 - <<'PY'
import fcntl, hashlib, json, os, re, shutil, signal, subprocess, tarfile, time, urllib.request
from pathlib import Path

os.umask(0o077)
TARGET_VERSION = '9547d22'
TARGET_COMMIT = '9547d22ecba81e51c1cb338c0c521559886a326d'
BUNDLE_SHA256 = '06f9c64fe79c03426211a15591161911e61f4b44dd18af3410ee63be0978d02d'
BASELINE_VERSION = 'f842928'
CANDIDATE_SHA256 = {'bin/golive-linux-amd64': 'e8648a7942d1e7c6a2c726033ab86efd786ca5801d302712d95cbc1a9bc7c493', 'bin/crm-linux-amd64': '95e3471b1004d9cbe38776bd8736c05e90e743822a65dbd5264ec5c8a2db1f80', 'bin/journal-linux-amd64': '654673394efe18a3f5f69c19326b6084bf0cc1331cd8012fe7dd2dd425b12c0e'}
PREFLIGHT_SHA256 = '1c69e8e07312ea57006b05641f5b7167f0d055ebde3e879489b8c2b7d9dfc40a'
CANDIDATE_PROFILE_SHA256 = '072ab8dbda962eff682fa56fcd049f70cd3717530e64a06bec97e66618878e7d'
CI_RECEIPT = {'commit': '9547d22ecba81e51c1cb338c0c521559886a326d', 'conclusion': 'success', 'run_id': 41176061770, 'url': 'https://github.com/chuanmingliu/full-duplex-go-live/actions/runs/41176061770', 'jobs': [{'name': 'quality', 'status': 'completed', 'conclusion': 'success'}, {'name': 'Go tests (race)', 'status': 'completed', 'conclusion': 'success'}, {'name': 'Go tests (normal)', 'status': 'completed', 'conclusion': 'success'}]}
CANDIDATE_FILES = {'bin/golive-linux-amd64': 'e8648a7942d1e7c6a2c726033ab86efd786ca5801d302712d95cbc1a9bc7c493', 'bin/crm-linux-amd64': '95e3471b1004d9cbe38776bd8736c05e90e743822a65dbd5264ec5c8a2db1f80', 'bin/journal-linux-amd64': '654673394efe18a3f5f69c19326b6084bf0cc1331cd8012fe7dd2dd425b12c0e', 'CALL-OPERATIONS.md': '184dff09f5176a22dac70ee4fa3e8331e14f6b54378f5351c538052faed49f29', 'HANDSET-LATENCY.md': 'd6e99090512fa4b62cb9d7f8556a58eaae3ab849d552cd22ab73ea3304c64ea9', 'VERSION': 'afc98457551bc53210245036cc85b52c56d0e4ee748c4e25a7c23fbaef514ff7', 'configs/telephony.json': '072ab8dbda962eff682fa56fcd049f70cd3717530e64a06bec97e66618878e7d', 'journal/decisions.json': 'cbeb6eded8c33b9226449a759fa38e5e76c25dac23422f4b5f5de0328ebdc675', 'journal/issues.json': '188faf99a11024063cafe8dc799eed4945e6e2cfd9bc0d4fb564b3529563c3b3', 'journal/retros/out-mvue7jic-wdgq.md': '30eb3adcbbf9e0ff67d856e9cb8f8bab77d4656aeb67bfb5189fafd2865141a6', 'journal/retros/out-mvuklw72-yaxy.md': 'ba820e6f64dd145bf8244510ab74685c7f375e54275a1a5869a060300c566a69', 'journal/retros/out-mvukudco-7zrb.md': '713a1fcba3749f2be21f18bf3ee430d4887dbf64b74fa2075d2a372707816fd5', 'journal/retros/out-mvuluygh-p1vy.md': '89f64024d9978b36cc9fe99f0bcfcb7ce38e91e593b78dbc6ed33b35f2db1303', 'journal/retros/out-mvumm8e2-t8in.md': '71c190b8f8a34ff5ceb99344a7b6eae9bc86d10114d8ded7c7f0abcbff6d6662', 'journal/retros/out-mvumqi79-ghis.md': '3788a3e1baa68a7228311154aa38d8cc44e8e1dce9b478e9ae59d58632db7af0', 'scripts/call-latency.sh': 'e6bd1022ad522792847b809f584ccfa10da7f5eadb32c874cef506ea7e7eadfe', 'scripts/call_latency.py': '41e5c29c16c5bf0011fcdad8a535290796d561f3be88904962d62abb78c4a850', 'scripts/phone-brain.sh': '390bc74c2d3a47cb3be9ccf88c127bcd48b4b218bac08e3785a17a60e331fe8a', 'scripts/sim-retro.py': 'a2169ca0865c1085d5cffb2dfca829904bbe8c889a79f86e270b5283f72d86c7', 'journal/retros/out-mvuoo3dk-dzip.md': 'feead9cfe33570efdd4f99671e3b69df826efcd5069fba05e2af59b81f410fa6', 'journal/retros/out-mvup3e10-odkv.md': '8ea1907529d4fcb59e3d55407d9b2703a40788e9b270a6fbf9f5588898007ab9', 'journal/retros/out-mvupyy53-pvt3.md': 'f6f8dee0e294a71758c68e51af29fa6143b097d6bd0c1ae6651a2cf8e6c8d2be', 'journal/retros/out-mvuqswhl-2dw7.md': '1fd429f4c8bdfce04af34370d84dc10d483f9e5601a353b6a6ba7adbbc1041f2', 'journal/retros/out-mvuqxt3d-i7nq.md': '2d24fbe9dc5d76af0199d99dcd412a4d78de0b45bb1e40fbb307b1624389568c', 'journal/retros/out-mvurvsmk-u9yk.md': '34691527d5a01bf97bf53f856d0eb2284ceb0c1659a09396f5ee22de45200bd4', 'journal/retros/out-mvvvt9l4-cauj.md': 'ce7ab0957bdadc183cf7cfe43437c8753cee5499ce2b7a4f3c1dd46700152639', 'journal/retros/out-mvvxxkse-wiqh.md': 'c006deac08bc50486fad1359b16ada74cb89bc835e2166228049607dfb68c617', 'journal/retros/out-mvw03ge0-j0y3.md': '5e634018dcd37d9ce76efbef3ca49d097da348ea44d551b357e3c0357f3a9851'}
phase = 'preflight'
if (not re.fullmatch(r'[0-9a-f]{7,40}', TARGET_VERSION) or
        not re.fullmatch(r'[0-9a-f]{40}', TARGET_COMMIT) or not TARGET_COMMIT.startswith(TARGET_VERSION) or
        not all(re.fullmatch(r'[0-9a-f]{64}', value) for value in [BUNDLE_SHA256, *CANDIDATE_SHA256.values()])):
    raise SystemExit('STOP: fill the reviewed target version, commit, bundle checksum and all three binary checksums first')
root = Path.home() / 'golive-telephony'
archive = Path.home() / ('golive/golive-latency-' + TARGET_VERSION + '.tar.gz')
release = Path.home() / ('golive-releases/codex-' + TARGET_VERSION + '-' + time.strftime('%Y%m%d-%H%M%S'))
lock = (root / '.codex-deploy.lock').open('a')
fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
expected = {'bin/golive-linux-amd64': '083caa6ad1cc8e07f5aac46c9105cc68a8e398e198e46062efb58c361b0a9837', 'bin/crm-linux-amd64': '09bb6dc115884fed6431e2dafcd338b022528610b96037ccc3a09e621870bebf', 'bin/journal-linux-amd64': 'b28307552ca7da0dac97c3a6d483dad7f0a15892bd346b3a0d57868022ce1106', 'CALL-OPERATIONS.md': '8c4fe93aab5a867e50701d6daedb94514767a20961f2aaad88c44c21e543b4e7', 'HANDSET-LATENCY.md': 'd6e99090512fa4b62cb9d7f8556a58eaae3ab849d552cd22ab73ea3304c64ea9', 'VERSION': '7e1ad632632d34031002668647f62b6d9c79e53fdacb30ad20193b64e6923d0d', 'configs/telephony.json': '072ab8dbda962eff682fa56fcd049f70cd3717530e64a06bec97e66618878e7d', 'journal/decisions.json': 'cbeb6eded8c33b9226449a759fa38e5e76c25dac23422f4b5f5de0328ebdc675', 'journal/issues.json': '2f82ab261b8a3debc2c2947ed7fb2ebc8c2ae26793d50b98fe4b553949e155c5', 'journal/retros/out-mvue7jic-wdgq.md': '30eb3adcbbf9e0ff67d856e9cb8f8bab77d4656aeb67bfb5189fafd2865141a6', 'journal/retros/out-mvuklw72-yaxy.md': 'ba820e6f64dd145bf8244510ab74685c7f375e54275a1a5869a060300c566a69', 'journal/retros/out-mvukudco-7zrb.md': '713a1fcba3749f2be21f18bf3ee430d4887dbf64b74fa2075d2a372707816fd5', 'journal/retros/out-mvuluygh-p1vy.md': '89f64024d9978b36cc9fe99f0bcfcb7ce38e91e593b78dbc6ed33b35f2db1303', 'journal/retros/out-mvumm8e2-t8in.md': '71c190b8f8a34ff5ceb99344a7b6eae9bc86d10114d8ded7c7f0abcbff6d6662', 'journal/retros/out-mvumqi79-ghis.md': '3788a3e1baa68a7228311154aa38d8cc44e8e1dce9b478e9ae59d58632db7af0', 'scripts/call-latency.sh': 'e6bd1022ad522792847b809f584ccfa10da7f5eadb32c874cef506ea7e7eadfe', 'scripts/call_latency.py': '41e5c29c16c5bf0011fcdad8a535290796d561f3be88904962d62abb78c4a850', 'scripts/phone-brain.sh': '390bc74c2d3a47cb3be9ccf88c127bcd48b4b218bac08e3785a17a60e331fe8a', 'scripts/sim-retro.py': 'a2169ca0865c1085d5cffb2dfca829904bbe8c889a79f86e270b5283f72d86c7', 'journal/retros/out-mvuoo3dk-dzip.md': 'feead9cfe33570efdd4f99671e3b69df826efcd5069fba05e2af59b81f410fa6', 'journal/retros/out-mvup3e10-odkv.md': '8ea1907529d4fcb59e3d55407d9b2703a40788e9b270a6fbf9f5588898007ab9', 'journal/retros/out-mvupyy53-pvt3.md': 'f6f8dee0e294a71758c68e51af29fa6143b097d6bd0c1ae6651a2cf8e6c8d2be', 'journal/retros/out-mvuqswhl-2dw7.md': '1fd429f4c8bdfce04af34370d84dc10d483f9e5601a353b6a6ba7adbbc1041f2', 'journal/retros/out-mvuqxt3d-i7nq.md': '2d24fbe9dc5d76af0199d99dcd412a4d78de0b45bb1e40fbb307b1624389568c', 'journal/retros/out-mvurvsmk-u9yk.md': '34691527d5a01bf97bf53f856d0eb2284ceb0c1659a09396f5ee22de45200bd4', 'journal/retros/out-mvvvt9l4-cauj.md': 'ce7ab0957bdadc183cf7cfe43437c8753cee5499ce2b7a4f3c1dd46700152639'}
expected_absent = ['journal/retros/out-mvvxxkse-wiqh.md', 'journal/retros/out-mvvy09vm-1d57.md', 'journal/retros/out-mvw03ge0-j0y3.md']
def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()
def require(ok, message):
    if not ok:
        raise RuntimeError(message)

POLICY_OWNER_COMMIT = '380c1a7350ec73094f01cc19006acff08b587719'
PINNED_RECEIPT_SHA256 = '6668f7ea49565f1d92bfc1e20b4515043d211b94da8a1013de6c0b0a9102f5c9'
PINNED_CONFIG = {'hangup-rules': {'version': 3, 'sha': '04a2f49a1de6', 'body_sha256': '04a2f49a1de6c22b0708c999df70c63279899d81947ae5e9e73f99a696ebca9a'}, 'openers': {'version': 3, 'sha': '1dc666c8cc12', 'body_sha256': '1dc666c8cc129ee8bad42be09dc2a216820ce4aa3dc5bb7942c6b2031e2b8bd2'}, 'quote-api': {'version': 4, 'sha': 'e0f534988eda', 'body_sha256': 'e0f534988eda239825ce70184b6f0d371d1b68f6f939affdfb5065fa453b8ef0'}, 'quote-rules': {'version': 2, 'sha': '854a7d3e9e25', 'body_sha256': '854a7d3e9e258472d356f21f5cfc6477c7afd2cbd429401363464302d1b2973b'}, 'quote-speech': {'version': 1, 'sha': '4a155d04c900', 'body_sha256': '4a155d04c9004102adf8a9d4a3954677f5c6cb41f0f9aef8d82df09ab628d7ef'}, 'quote-plans': {'version': 7, 'sha': 'f2142bd9cddd', 'body_sha256': 'f2142bd9cddd813e00cb898f837206f8975c59d1ef4257aa12d143eecf1d1afb'}}

def verify_pinned_quote_policy(root, crm_data=None):
    receipt_dir = root/'codex-latency/follow-selection-380c1a7-20261030-1'
    receipt_path = receipt_dir/'enable-attempt.json'
    require(digest(receipt_path) == PINNED_RECEIPT_SHA256, 'enabled policy receipt changed')
    receipt = json.loads(receipt_path.read_text())
    require(receipt.get('candidate') == POLICY_OWNER_COMMIT and receipt.get('operation') == 'enable' and
            receipt.get('stage') == 'verified', 'enabled policy receipt identity differs')
    require(not (receipt_dir/'rollback-attempt.json').exists(), 'policy rollback was attempted')
    require(receipt.get('expected') == PINNED_CONFIG['quote-plans'] and
            receipt.get('protected_configs') == {n:s for n,s in PINNED_CONFIG.items() if n != 'quote-plans'},
            'receipt policy inventory differs')
    require(receipt.get('change_path') == 'plans.standard.prequote.follow_selection_max' and receipt.get('value') == 8,
            'enabled policy is not the reviewed change')
    require(digest(receipt_dir/'candidate-v7.json') == PINNED_CONFIG['quote-plans']['body_sha256'] and
            digest(receipt_dir/'original-v6.json') == receipt['baseline']['body_sha256'], 'saved config payload changed')
    if crm_data is None:
        rows = subprocess.check_output(['ss','-lntpH','sport = :8791'], text=True)
        pids = set(re.findall(r'pid=(\d+)', rows))
        require(len(pids) == 1, 'CRM listener identity ambiguous')
        proc = Path('/proc')/pids.pop()
        argv = [os.fsdecode(x) for x in (proc/'cmdline').read_bytes().split(b'\0') if x]
        values = [v.split('=',1)[1] for v in argv if v.startswith('-dir=')]
        values += [argv[i+1] for i,v in enumerate(argv[:-1]) if v == '-dir']
        require(len(values) == 1, 'CRM data directory ambiguous')
        crm_data = ((proc/'cwd').resolve()/values[0]).resolve()
    token = (crm_data/'api.token').read_text().strip()
    require(bool(token), 'CRM token unavailable')
    def request(path):
        req = urllib.request.Request('http://127.0.0.1:8791/api/configs'+path,
                                     headers={'Authorization':'Bearer '+token})
        with urllib.request.urlopen(req, timeout=15) as r: return json.load(r)
    rows = request('')['configs'];names = [row['name'] for row in rows]
    require(len(names) == len(set(names)) and set(names) == set(PINNED_CONFIG), 'configuration inventory changed')
    for name, expected in PINNED_CONFIG.items():
        current = request('/'+name)['config']
        require(current.get('name') == name and type(current.get('version')) is int, 'configuration identity invalid')
        raw_path = crm_data/'configs'/name/('v%04d.json' % current['version'])
        raw_sha = digest(raw_path)
        stamp = {'version':current['version'], 'sha':current['sha'], 'body_sha256':raw_sha}
        require(stamp == expected and raw_sha[:12] == current['sha'] and json.loads(raw_path.read_text()) == current['body'],
                'configuration body or version changed: '+name)
    return {'quote_plans':PINNED_CONFIG['quote-plans'], 'enable_receipt_sha256':PINNED_RECEIPT_SHA256,
            'protected_configurations_verified':len(PINNED_CONFIG)-1, 'follow_selection_max':8}

def validate_profile_change(previous_raw,candidate_raw):
    require(previous_raw==candidate_raw,'main profile changed; release preserves d852 bytes')
    require(hashlib.sha256(candidate_raw.encode()).hexdigest()=='072ab8dbda962eff682fa56fcd049f70cd3717530e64a06bec97e66618878e7d','main profile is not the reviewed d852 bytes')

def option_from(argv, name, default=None):
    for index, value in enumerate(argv):
        if value == name and index + 1 < len(argv):
            return argv[index + 1]
        if value.startswith(name + '='):
            return value.split('=', 1)[1]
    if default is not None:
        return default
    raise RuntimeError('cannot preserve option: ' + name)

def identity(pid):
    stat = (Path('/proc') / str(pid) / 'stat').read_text()
    return str(pid), stat.rsplit(')', 1)[1].split()[19]

def process(pid):
    path = Path('/proc') / str(pid)
    start = identity(pid)
    result = {'id': start, 'pid': str(pid), 'cwd': path.joinpath('cwd').resolve(strict=True),
              'argv': [os.fsdecode(v) for v in path.joinpath('cmdline').read_bytes().split(b'\0') if v],
              'env': dict(os.fsdecode(v).split('=', 1) for v in path.joinpath('environ').read_bytes().split(b'\0') if b'=' in v)}
    require(path.stat().st_uid == os.getuid(), 'service is not owned by the runner user')
    require(identity(pid) == start, 'service changed while reading configuration')
    return result

def port_pids(port):
    # ss can observe a new/closing socket before its users/PID association.
    # Retry that indeterminate state; it never counts as an absent listener.
    until = time.monotonic() + 3
    while True:
        result = subprocess.run(['ss', '-lntpH', 'sport = :%s' % port],
                                capture_output=True, text=True, check=True)
        rows = [row for row in result.stdout.splitlines() if row.strip()]
        owners = [set(re.findall(r'pid=(\d+)', row)) for row in rows]
        if not rows:
            return set()
        if all(owners):
            return set().union(*owners)
        if time.monotonic() >= until:
            raise RuntimeError('port %s: listener owner unresolved after 3 seconds during %s' % (port, phase))
        time.sleep(0.2)

def on_port(port):
    pids = port_pids(port)
    require(len(pids) == 1, 'port %s: expected one listener owner, found %s during %s' % (port, len(pids), phase))
    return process(pids.pop())

def resolved(proc, value):
    return (proc['cwd'] / value).resolve(strict=True)

def pid_file(name):
    path = root / name
    if not path.exists():
        return None
    value = path.read_text().strip()
    require(value.isdecimal(), 'invalid supervisor PID file')
    return value

def alive(pid):
    if pid is None:
        return False
    try:
        os.kill(int(pid), 0)
        return True
    except ProcessLookupError:
        return False
    # Permission errors are not evidence of absence.

def runtime_keys(values):
    prefixes = ('GOLIVE_', 'DEEPSEEK_', 'TENCENT_', 'MINIMAX_', 'FISH_', 'OPENAI_')
    names = {'HTTP_PROXY', 'HTTPS_PROXY', 'ALL_PROXY', 'NO_PROXY', 'http_proxy',
             'https_proxy', 'all_proxy', 'no_proxy', 'SSL_CERT_FILE', 'SSL_CERT_DIR',
             'GODEBUG', 'GOMAXPROCS'}
    return {key for key in values if key.startswith(prefixes) or key in names}

def get_json(url):
    with urllib.request.urlopen(url, timeout=5) as response:
        return json.load(response)

def demo_health():
    return get_json('http://127.0.0.1:%s/healthz' % demo_port)

def health():
    with urllib.request.urlopen('http://127.0.0.1:8765/healthz', timeout=5) as response:
        return json.load(response)
before = health()
if before.get('status') != 'ok' or before.get('sessions') != 0 or before.get('version') != BASELINE_VERSION:
    raise SystemExit('STOP: service is busy or its version changed')
if digest(archive) != BUNDLE_SHA256:
    raise SystemExit('STOP: bundle checksum mismatch')
def verify_baseline_files():
    for name, sha in expected.items():
        require(digest(root / name) == sha, 'deployed file changed: ' + name)
    for name in expected_absent:
        require(not (root / name).exists() and not (root / name).is_symlink(),
                'previously absent deployment file appeared: ' + name)

preflight_path=root/'codex-latency/preflight-opener-release-20261031-5.json'
require(digest(preflight_path)==PREFLIGHT_SHA256,'preflight snapshot changed')
preflight=json.loads(preflight_path.read_text())
require(preflight['baseline_commit']=='f842928b3dd0862a6f9a6d369b040c21af46101e' and
        preflight['notes_commit']=='f842928b3dd0862a6f9a6d369b040c21af46101e' and
        preflight['hashes']==expected,'preflight baseline differs')
require(CI_RECEIPT['commit']==TARGET_COMMIT and CI_RECEIPT['conclusion']=='success','candidate CI mismatch')
require(type(CI_RECEIPT['run_id']) is int and CI_RECEIPT['run_id']>0,'CI run missing')
require(len(CI_RECEIPT['jobs'])==3 and {j['name'] for j in CI_RECEIPT['jobs']}=={'quality','Go tests (normal)','Go tests (race)'} and
        all(j.get('status')=='completed' and j.get('conclusion')=='success' for j in CI_RECEIPT['jobs']),'full candidate CI is not complete')

def verify_archives():
    fingerprints=preflight['archive_fingerprints']
    require(len(fingerprints)==16,'sixteen archived calls required')
    require(set(fingerprints)==set(case['call'] for case in preflight['calls']),'archive inventory differs')
    for call,record in fingerprints.items():
        source=Path(record['directory'])
        require(source.resolve(strict=True).is_relative_to((Path.home()/'golive-archive').resolve(strict=True)),'archive escaped root')
        require(digest(source/'manifest.json')==record['manifest_sha256'],'archive manifest changed')
        require(set(record['files'])=={'audio.wav','transcript.txt','call.json','session.json','golive.log'},'archive file inventory differs')
        for name,stamp in record['files'].items():
            path=source/name
            require(path.is_file() and not path.is_symlink() and path.stat().st_size==stamp['bytes'] and digest(path)==stamp['sha256'],'archive evidence changed')

SAVED_IDENTITY = {'instructions_sha256': '639dcecf24dddc73274a7412c017e36258b51679c2fd3ad4e72a595453aa8d25', 'greeting_sha256': 'fa7c33a6b9c732413d932a7453c5c85d4936fcdd8515822bf92e3fc07e2f4df8', 'voice_sha256': '65d9d6c5c7c2d5c29e38b777edf8da1dbd764f67deeeaa230724967cafe4e684'}
def saved_context_identity(kept, call, session):
    require(isinstance(kept,dict) and isinstance(call,dict) and isinstance(session,dict),'saved context shape invalid')
    call_id=kept.get('call')
    require(isinstance(call_id,str) and re.fullmatch(r'out-[a-z0-9]+-[a-z0-9]+',call_id),'saved call ID invalid')
    require(call.get('id')==call_id and session.get('id')==call.get('session'),'saved call/session pairing differs')
    request=session.get('session',{})
    require(isinstance(request,dict),'saved session request absent')
    actual={}
    for key,source in [('instructions',request.get('instructions')),('voice',request.get('audio',{}).get('output',{}).get('voice','')),('greeting',call.get('greeting'))]:
        value=kept.get(key,'' if key=='voice' else None)
        require(isinstance(value,str) and isinstance(source,str) and value==source,'saved '+key+' differs from recorded call')
        actual[key+'_sha256']=hashlib.sha256(value.encode()).hexdigest()
    require(actual==SAVED_IDENTITY,'saved prompt, voice or greeting changed')
    return dict(call=call_id,session=call['session'],**actual)
def verify_saved_context(fingerprints):
    kept=json.loads((Path.home()/'golive-last-call.json').read_text())
    call_id=kept.get('call')
    require(isinstance(call_id,str) and call_id in fingerprints,'saved call is not a pinned completed archive')
    record=fingerprints[call_id];source=Path(record['directory'])
    require(source.resolve(strict=True).is_relative_to((Path.home()/'golive-archive').resolve(strict=True)),'saved archive escaped root')
    for name in ('call.json','session.json'):
        p=source/name
        require(p.is_file() and not p.is_symlink() and digest(p)==record['files'][name]['sha256'],'saved context archive changed')
    return saved_context_identity(kept,json.loads((source/'call.json').read_text()),json.loads((source/'session.json').read_text()))

def verify_saved_prompt():
    identity=verify_saved_context(preflight['archive_fingerprints'])
    require(identity==preflight['saved_call_identity'],'saved call changed since preflight')
    return identity
verify_archives()
verify_saved_prompt()

verify_baseline_files()
if not (root / '.phone-brain.record').exists():
    raise SystemExit('STOP: recording is no longer enabled')
require(not (root / 'codex-latency/deployment-9547d22-opener-release-20261031-5.json').exists(), 'deployment receipt already exists; inspect instead of retrying')
release.mkdir(parents=True, exist_ok=False)
stage, backup = release / 'candidate', release / 'previous'
stage.mkdir(); backup.mkdir()
required_files = {'journal/retros/out-mvukudco-7zrb.md', 'journal/issues.json', 'journal/retros/out-mvup3e10-odkv.md', 'scripts/phone-brain.sh', 'journal/retros/out-mvumqi79-ghis.md', 'bin/journal-linux-amd64', 'journal/retros/out-mvumm8e2-t8in.md', 'bin/golive-linux-amd64', 'CALL-OPERATIONS.md', 'journal/retros/out-mvuluygh-p1vy.md', 'journal/decisions.json', 'journal/retros/out-mvuklw72-yaxy.md', 'journal/retros/out-mvuoo3dk-dzip.md', 'scripts/call-latency.sh', 'bin/crm-linux-amd64', 'journal/retros/out-mvupyy53-pvt3.md', 'journal/retros/out-mvuqxt3d-i7nq.md', 'journal/retros/out-mvurvsmk-u9yk.md', 'journal/retros/out-mvuqswhl-2dw7.md', 'journal/retros/out-mvvxxkse-wiqh.md', 'scripts/sim-retro.py', 'journal/retros/out-mvw03ge0-j0y3.md', 'VERSION', 'HANDSET-LATENCY.md', 'journal/retros/out-mvvvt9l4-cauj.md', 'scripts/call_latency.py', 'journal/retros/out-mvue7jic-wdgq.md', 'configs/telephony.json'}
allowed = required_files
with tarfile.open(archive) as bundle:
    members = bundle.getmembers()
    for item in members:
        path = Path(item.name)
        if path.is_absolute() or '..' in path.parts or not item.isfile():
            raise SystemExit('STOP: unsupported archive member')
        if item.isfile() and item.name not in allowed:
            raise SystemExit('STOP: unexpected deployment file')
    names = [item.name for item in members]
    require(len(names) == len(set(names)), 'bundle contains duplicate file members')
    require(set(names) == required_files, 'bundle does not match the reviewed unique allowlist')
    bundle.extractall(stage, filter='data')
files = sorted(item.name for item in members if item.isfile())
require(required_files <= set(files), 'bundle lacks required binaries, reports, operations or call notes')
require(len(files) == len(set(files)), 'bundle contains duplicate file members')
require((stage / 'VERSION').read_text().strip() == TARGET_COMMIT, 'bundle VERSION differs from the reviewed commit')
binary_names = ('bin/golive-linux-amd64', 'bin/crm-linux-amd64', 'bin/journal-linux-amd64')
candidate_hashes = {name: digest(stage / name) for name in binary_names}
require(candidate_hashes == CANDIDATE_SHA256, 'candidate binary hashes differ from the reviewed build')
require(set(CANDIDATE_FILES)==required_files and all(digest(stage/name)==value for name,value in CANDIDATE_FILES.items()),'candidate payload differs from reviewed build')
require(digest(stage/'configs/telephony.json')==CANDIDATE_PROFILE_SHA256,'candidate profile hash mismatch')
validate_profile_change((root/'configs/telephony.json').read_text(),(stage/'configs/telephony.json').read_text())
existed = []
for name in files:
    old = root / name
    if old.exists():
        target = backup / name
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(old, target)
        existed.append(name)
# Retain the executable actually serving calls even if an earlier bundle was
# copied over its on-disk pathname without a restart.
main = on_port(8765)
pid = main['pid']
require(main['cwd'] == root, 'phone service has an unexpected working directory')
argv = main['argv']
main_addr = option_from(argv, '-addr')
bind, _, port = main_addr.rpartition(':')
require(port == '8765' and bind, 'phone service has an unexpected listen address')
profile = resolved(main, option_from(argv, '-profile'))
envfile = resolved(main, option_from(argv, '-env'))
protected = {path: digest(path) for path in (profile, envfile)}
require(profile==(root/'configs/telephony.json').resolve(strict=True),'main is not using reviewed profile')
require(str(profile)==preflight['profiles']['8765']['profile'] and digest(profile)==preflight['profiles']['8765']['profile_sha256'],'main profile changed since preflight')
require(str(envfile)==preflight['profiles']['8765']['envfile'] and digest(envfile)==preflight['profiles']['8765']['envfile_sha256'],'main environment file changed since preflight')
original_env = main['env']
require(original_env.get('GOLIVE_REALTIME_STORE_CALLS') == '1' and original_env.get('GOLIVE_STORE_DIR'),
        'recording is not enabled in the running service')
require(original_env.get('GOLIVE_STORE_TTL_HOURS', '').isdecimal(), 'cannot prove the running recording retention')
store, ttl = original_env['GOLIVE_STORE_DIR'], original_env['GOLIVE_STORE_TTL_HOURS']
require(ttl == '720', 'recording retention is no longer the reviewed 720 hours')
actual_binary = backup / 'bin/golive-linux-amd64'
actual_binary.parent.mkdir(parents=True, exist_ok=True)
shutil.copy2(Path('/proc', pid, 'exe'), actual_binary)
if 'bin/golive-linux-amd64' not in existed:
    existed.append('bin/golive-linux-amd64')

# Keep runtime overrides in memory only; do not print or persist any environment.
env = {k: v for k, v in original_env.items() if k not in {'_', 'PWD', 'OLDPWD', 'SHLVL'}}
env.update(JOURNAL_TARGET_MS='1200', PROFILE=str(profile), ENVFILE=str(envfile),
           PORT=port, BIND=bind, CALLS_DIR=store, CALLS_TTL_HOURS=ttl, GOLIVE_STORE_TTL_HOURS=ttl)

# restart also restarts the journal; retain the running journal's actual paths.
journal_pid = pid_file('.phone-brain.journal.pid')
require(alive(journal_pid), 'cannot identify the running journal')
journal_proc = process(journal_pid)
jargv = journal_proc['argv']
jaddr = option_from(jargv, '-addr')
require(journal_pid in port_pids(int(jaddr.rpartition(':')[2])), 'journal PID is not its listener')
journal_url = 'http://' + jaddr + '/api/overview'
require(resolved(journal_proc, option_from(jargv, '-notes')) == root / 'journal', 'custom journal notes path cannot be preserved')
require(option_from(jargv, '-ui', '') == '', 'custom journal UI cannot be preserved')
require(option_from(jargv, '-retention-hours') == ttl, 'journal and phone retention settings differ')
require(resolved(journal_proc, option_from(jargv, '-store')) == resolved(main, store), 'journal and phone recording paths differ')
log_path = resolved(journal_proc, option_from(jargv, '-log'))
require(Path('/proc', pid, 'fd/1').resolve(strict=True) == log_path, 'journal does not follow the phone output log')
sim_dials = Path(option_from(jargv, '-sim-dials'))
require(sim_dials.parts[-2:] == ('data', 'dials.jsonl'), 'custom simulator dial path cannot be preserved')
sim_dir = (journal_proc['cwd'] / sim_dials).resolve().parent.parent

env.update(JOURNAL_ADDR=jaddr, JOURNAL_DATA=str(resolved(journal_proc, option_from(jargv, '-data'))),
           ARCHIVE_DIR=str(resolved(journal_proc, option_from(jargv, '-archive'))),
           LAST_CALL=str(resolved(journal_proc, option_from(jargv, '-last-call'))),
           SIM_DIR=str(sim_dir), JOURNAL_REPO=option_from(jargv, '-repo'), LOGFILE=str(log_path))

# CRM is independent of phone-brain restart. Keep its own argv/environment,
# allow its 50-second HTTP shutdown to finish, and never replace its data.
crm = on_port(8791)
require(pid_file('.phone-brain.crm.pid') == crm['pid'], 'CRM PID file and listener differ')
require(crm['cwd'] == root, 'CRM has an unexpected working directory')
crm_data = resolved(crm, option_from(crm['argv'], '-dir'))
crm_originator_env = resolved(crm, option_from(crm['argv'], '-originator-env'))
protected[crm_originator_env] = digest(crm_originator_env)
protected[crm_data / 'api.token'] = digest(crm_data / 'api.token')
crm_log = Path('/proc', crm['pid'], 'fd/1').resolve(strict=True)
require(Path('/proc', crm['pid'], 'fd/2').resolve(strict=True) == crm_log, 'CRM stdout/stderr destinations differ')
require(get_json('http://127.0.0.1:8791/healthz').get('status') == 'ok', 'CRM is unhealthy before deployment')
verify_pinned_quote_policy(root, crm_data)

def binary_version(path):
    result = subprocess.run([str(path), '-version'], capture_output=True, text=True, timeout=5, check=True)
    value = result.stdout.strip()
    require(re.fullmatch(r'[0-9a-zA-Z._+-]+', value), 'CRM version response is not an identifier')
    return value

crm_before_version = binary_version(Path('/proc', crm['pid'], 'exe'))
require(binary_version(stage / 'bin/crm-linux-amd64') == TARGET_VERSION, 'CRM candidate build has the wrong version')
for proc, name in ((main, 'bin/golive-linux-amd64'), (crm, 'bin/crm-linux-amd64'),
                   (journal_proc, 'bin/journal-linux-amd64')):
    require(digest(Path('/proc', proc['pid'], 'exe')) == expected[name], 'running executable changed: ' + name)
    target = backup / name
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(Path('/proc', proc['pid'], 'exe'), target)
    if name not in existed:
        existed.append(name)
rollback_hashes = {name: digest(backup / name) for name in binary_names}

# Read the existing desk endpoint; its process and data must stay unchanged.
desk = on_port(8792)
require(pid_file('.phone-brain.desk.pid') == desk['pid'], 'desk PID file and listener differ')
desk_addr = option_from(desk['argv'], '-addr')
talker_url = option_from(desk['argv'], '-talker')
talker_playground = original_env.get('TALKER_PLAYGROUND_URL', 'http://' + desk_addr.split(',')[-1].strip() + '/#/talker')
require(option_from(jargv, '-talker') == talker_url and
        option_from(jargv, '-talker-playground') == talker_playground,
        'existing journal Talker endpoints differ from the desk')
env.update(TALKER_URL=talker_url, DESK_ADDR=desk_addr, TALKER_PLAYGROUND_URL=talker_playground)

def simulator_identity():
    result = subprocess.run(['docker', 'ps', '-q', '--filter', 'label=com.docker.compose.project=golive-user-simulator'],
                            capture_output=True, text=True, check=True)
    ids = sorted(result.stdout.split())
    require(ids, 'simulator containers could not be identified')
    details = subprocess.run(['docker', 'inspect', '--format', '{{.Id}} {{.State.StartedAt}} {{.State.Running}} {{.RestartCount}}', *ids],
                             capture_output=True, text=True, check=True)
    return sorted(details.stdout.splitlines())

simulator_before = simulator_identity()
require(simulator_before==preflight['simulator_identity'],'simulator changed since preflight')
require(desk['id']==(preflight['processes']['8792']['pid'],preflight['processes']['8792']['start']),'Desk changed since preflight')
def verify_untouched():
    require(on_port(8792)['id'] == desk['id'], 'desk restarted or changed during deployment')
    require(simulator_identity() == simulator_before, 'simulator containers changed during deployment')

# dstart shares ENVFILE and provider environment with the phone launcher.
# Refuse a deployment when that launcher cannot preserve both services.
demo = None
demo_enabled = (root / '.phone-brain.demo').is_file()
if demo_enabled:
    demo_supervisor = pid_file('.phone-brain.demo.pid')
    require(alive(demo_supervisor), 'enabled demo has no live supervisor')
    demo_addr = option_from(process(demo_supervisor)['argv'], '-addr')
    demo_bind, _, demo_port = demo_addr.rpartition(':')
    require(demo_bind == '127.0.0.1' and demo_port.isdecimal(), 'custom demo bind cannot be preserved')
    demo_port = int(demo_port)
    demo = on_port(demo_port)
    require(demo['cwd'] == root and option_from(demo['argv'], '-addr') == demo_addr, 'demo launch configuration changed')
    require(resolved(demo, option_from(demo['argv'], '-env')) == envfile, 'phone and demo use different environment files')
    demo_profile = resolved(demo, option_from(demo['argv'], '-profile'))
    protected[demo_profile] = digest(demo_profile)
    require(demo_profile==(root/'configs/tencent-deepseek-minimax.json').resolve(strict=True) and demo_profile!=profile,'demo must use its separate unchanged profile')
    require(str(demo_profile)==preflight['profiles']['9080']['profile'] and digest(demo_profile)==preflight['profiles']['9080']['profile_sha256'],'demo profile changed since preflight')
    demo_before = demo_health()
    require(demo_before.get('status') == 'ok' and demo_before.get('sessions') == 0, 'demo is busy or unhealthy')
    require(demo_before.get('version') == before['version'] and
            digest(Path('/proc', demo['pid'], 'exe')) == digest(Path('/proc', pid, 'exe')),
            'phone and demo run different binaries; shared rollback would change the demo build')
    demo_env = demo['env']
    demo_unset = {'GOLIVE_STORE_DIR', 'GOLIVE_REALTIME_STORE_CALLS', 'GOLIVE_LAST_CALL_FILE', 'GOLIVE_TELEMETRY_URL'}
    demo_override = {'GOLIVE_LOG_LEVEL', 'GOLIVE_MAX_SESSIONS', 'GOLIVE_WEB_ROOT'}
    require(not any(key in demo_env for key in demo_unset), 'demo has settings the launcher always removes')
    require(demo_env.get('GOLIVE_WEB_ROOT') == 'web', 'custom demo web root cannot be preserved')
    # start_golive assigns DEEPSEEK_LANES inline; dstart does not inherit that
    # assignment. Keep that distinction when the profile proves the same value.
    inline_lanes = 'DEEPSEEK_LANES' in original_env and 'DEEPSEEK_LANES' not in demo_env
    if inline_lanes:
        copies = re.search(r'"llm_hedge": *([0-9]+)', profile.read_text())
        derived_lanes = original_env.get('GOLIVE_LLM_HEDGE') or (copies.group(1) if copies else '4')
        require(original_env['DEEPSEEK_LANES'] == derived_lanes, 'main lane override cannot be preserved separately from the demo')
        env.pop('DEEPSEEK_LANES', None)
    for key in runtime_keys(original_env) | runtime_keys(demo_env):
        if key == 'DEEPSEEK_LANES' and inline_lanes:
            continue
        if key not in demo_unset | demo_override:
            require(original_env.get(key) == demo_env.get(key), 'incompatible phone/demo runtime setting: ' + key)
    require(demo_env.get('GOLIVE_MAX_SESSIONS', '').isdecimal(), 'cannot preserve the demo session limit')
    env.update(DEMO_PROFILE=str(demo_profile), DEMO_PORT=str(demo_port),
               DEMO_LOG_LEVEL=demo_env.get('GOLIVE_LOG_LEVEL', 'info'),
               DEMO_MAX_SESSIONS=demo_env['GOLIVE_MAX_SESSIONS'])

# Predict the journal's inherited environment before modifying live files.
# phone-brain exports the same recording/tool settings already captured above.
journal_expected_env = {key: value for key, value in journal_proc['env'].items()
                        if key not in {'_', 'PWD', 'OLDPWD', 'SHLVL'}}
journal_expected_env.update(TALKER_URL=talker_url, DESK_ADDR=desk_addr, TALKER_PLAYGROUND_URL=talker_playground)
require(env == journal_expected_env, 'phone launcher cannot preserve the journal environment exactly')

require(demo_enabled and demo is not None,'reviewed demo must remain enabled')
for service_port,service in ((8765,main),(9080,demo),(8790,journal_proc),(8791,crm)):
    record=preflight['processes'][str(service_port)]
    require(service['id']==(record['pid'],record['start']),'service changed since preflight')
    require(digest(Path('/proc',service['pid'],'exe'))==record['exe_sha256'],'service executable changed since preflight')
for service_port,service in ((8765,main),(9080,demo)):
    record=preflight['profiles'][str(service_port)]
    require(digest(Path('/proc',service['pid'],'environ'))==record['process_environment_sha256'],'inherited environment changed since preflight')
    require(digest(Path('/proc',service['pid'],'cmdline'))==record['argv_sha256'],'service argv changed since preflight')
baseline_protected = dict(protected)

def check_protected():
    verify_saved_prompt()
    verify_untouched()
    require((root / '.phone-brain.record').is_file(), 'recording marker changed')
    require((root / '.phone-brain.demo').is_file() == demo_enabled, 'demo enablement changed')
    for path, sha in protected.items():
        require(digest(path) == sha, 'an active profile or environment file changed')

def demo_idle(rollback=False):
    if demo:
        try:
            h = demo_health()
        except (OSError, ValueError):
            require(rollback and await_stopped(demo_service=True), 'demo is unavailable and not proven stopped')
            return
        require(h.get('status') == 'ok' and h.get('sessions') == 0, 'demo is busy or unhealthy before restart')

def stopped(demo_service=False):
    service, service_port = (demo, demo_port) if demo_service else (main, 8765)
    supervisor = '.phone-brain.demo.pid' if demo_service else '.phone-brain.pid'
    stop_marker = '.phone-brain.demo.stop' if demo_service else '.phone-brain.stop'
    service_addr = option_from(service['argv'], '-addr')
    other_addr = main_addr if demo_service else (demo_addr if demo else None)
    # No listener alone is insufficient: a keeper may be retrying, or a process
    # may hold established calls after its listener closes. Inspect both.
    if port_pids(service_port) or alive(pid_file(supervisor)):
        return False
    for path in Path('/proc').iterdir():
        if not path.name.isdecimal():
            continue
        try:
            if path.stat().st_uid != os.getuid():
                continue
            args = [os.fsdecode(v) for v in (path / 'cmdline').read_bytes().split(b'\0') if v]
            if not args:
                continue  # An exited zombie cannot own calls or restart a child.
            addr = option_from(args, '-addr', '')
            if other_addr and addr == other_addr:
                continue
            cwd = (path / 'cwd').resolve(strict=True)
            executable = (path / 'exe').readlink().name
            golive = executable.startswith('golive')
            keeper = 'keepalive' in args and any(Path(v).name == stop_marker for v in args)
            same_args = all(option_from(args, flag, '') in {str(resolved(service, option_from(service['argv'], flag))),
                            option_from(service['argv'], flag)} for flag in ('-profile', '-env'))
            if (golive or keeper) and (addr == service_addr or same_args or cwd == root):
                return False
        except FileNotFoundError:
            continue  # Process exited during the scan.
        except (PermissionError, ProcessLookupError):
            return False  # Unreadable is not proof of absence.
    return not port_pids(service_port) and not alive(pid_file(supervisor))

def await_stopped(demo_service=False):
    until = time.monotonic() + 45
    while time.monotonic() < until:
        if stopped(demo_service):
            time.sleep(1)
            if stopped(demo_service):
                return True
        time.sleep(1)
    return False

def verify_runtime(proc, baseline):
    require(proc['argv'][1:] == baseline['argv'][1:], 'service startup arguments changed')
    require(clean_env(proc['env']) == clean_env(baseline['env']), 'service inherited environment changed')
    require(option_from(proc['argv'], '-addr') == option_from(baseline['argv'], '-addr'), 'service listen address changed')
    for flag in ('-profile', '-env'):
        require(resolved(proc, option_from(proc['argv'], flag)) == resolved(baseline, option_from(baseline['argv'], flag)),
                'service configuration path changed: ' + flag)
    for key in runtime_keys(baseline['env']) | runtime_keys(proc['env']):
        require(proc['env'].get(key) == baseline['env'].get(key), 'service runtime setting changed: ' + key)

def verify_demo(version):
    if demo:
        h = demo_health()
        require(h.get('status') == 'ok' and h.get('version') == version, 'demo did not become the expected healthy build')
        verify_runtime(on_port(demo_port), demo)

manifest = {'candidate': TARGET_VERSION, 'previous_health': before, 'files': files,
            'existed': existed, 'bundle_sha256': digest(archive), 'state': 'prepared',
            'candidate_binary_sha256': candidate_hashes, 'rollback_binary_sha256': rollback_hashes,
            'previous_crm_version': crm_before_version, 'phase': 'prepared',
            'preflight_sha256':PREFLIGHT_SHA256,'ci':CI_RECEIPT,
            'profile_preservation':{'path':'configs/telephony.json','sha256':CANDIDATE_PROFILE_SHA256,'quote_readout_warm_max':4},
            'profile_scope':{'main':str(profile),'demo':str(demo_profile)},'archived_calls_verified':16,'archive_files_verified':80}
def save():
    (release / 'deployment.json').write_text(json.dumps(manifest, indent=2) + '\n')
    shutil.copy2(release / 'deployment.json', root / 'codex-latency/deployment-9547d22-opener-release-20261031-5.json')
save()
print(json.dumps({'release': str(release), 'state': 'prepared', 'phase': manifest['phase']}), flush=True)
def persist_diagnostics():
    try:
        save()
    except OSError as error:
        # Recovery must not depend on free space for its diagnostic record.
        print('Manifest diagnostic write failed: ' + type(error).__name__, flush=True)

def set_phase(value):
    global phase
    phase = value
    manifest['phase'] = value
    if cutover_started or recovering:
        persist_diagnostics()
    else:
        save()

def restart(label, allow_stopped=False):
    set_phase(label + '.guards')
    check_protected()
    require(get_json('http://127.0.0.1:8791/healthz').get('status') == 'ok', 'CRM must be running before the phone launcher restart')
    require(pid_file('.phone-brain.crm.pid') == on_port(8791)['pid'], 'CRM PID file no longer identifies its listener')
    demo_idle(rollback=label == 'restart-rollback')
    if allow_stopped:
        require(stopped(), 'phone service is no longer proven stopped')
    else:
        h = health()
        require(h.get('status') == 'ok' and h.get('sessions') == 0, 'phone service is busy or unhealthy before restart')
    set_phase(label + '.command')
    with (release / (label + '.log')).open('wb') as log:
        result = subprocess.run(['bash', 'scripts/phone-brain.sh', 'restart'], cwd=root,
                                env=env, stdout=log, stderr=subprocess.STDOUT, timeout=120)
    if result.returncode:
        raise RuntimeError('restart command failed; diagnostic log retained on the box')
def await_version(version):
    until = time.monotonic() + 35
    while time.monotonic() < until:
        try:
            h = health()
            if h.get('status') == 'ok' and h.get('version') == version:
                return h
        except (OSError, ValueError):
            pass
        time.sleep(2)
    raise RuntimeError('running service version did not become ' + version)
def same_process(proc):
    try:
        return identity(proc['pid']) == proc['id']
    except FileNotFoundError:
        return False

def clean_env(values):
    return {key: value for key, value in values.items() if key not in {'_', 'PWD', 'OLDPWD', 'SHLVL'}}

def verify_binary(proc, name, hashes):
    require(digest(Path('/proc', proc['pid'], 'exe')) == hashes[name], 'running binary hash differs: ' + name)
    require(same_process(proc), 'service changed while verifying its binary')

def verify_crm(hashes, version):
    active = on_port(8791)
    require(pid_file('.phone-brain.crm.pid') == active['pid'], 'CRM PID file differs from the active process')
    require(active['cwd'] == crm['cwd'] and active['argv'][1:] == crm['argv'][1:], 'CRM startup arguments changed')
    require(clean_env(active['env']) == clean_env(crm['env']), 'CRM inherited environment changed')
    verify_binary(active, 'bin/crm-linux-amd64', hashes)
    require(binary_version(Path('/proc', active['pid'], 'exe')) == version, 'CRM running version differs')
    require(get_json('http://127.0.0.1:8791/healthz').get('status') == 'ok', 'CRM is unhealthy')
    verify_pinned_quote_policy(root, crm_data)

crm_child = None
crm_current = crm

def restart_crm(label, hashes, version):
    global crm_child, crm_current
    set_phase(label + '.crm.guards')
    check_protected()
    demo_idle(rollback=label == 'rollback')
    try:
        h = health()
        require(h.get('status') == 'ok' and h.get('sessions') == 0, 'phone service is busy before CRM restart')
    except (OSError, ValueError):
        require(label == 'rollback' and await_stopped(), 'phone service is unavailable and not proven stopped before CRM restart')
    if crm_child is not None:
        crm_child.poll()
    set_phase(label + '.crm.inspect')
    listeners = port_pids(8791)
    active = on_port(8791) if listeners else None
    if active is None and same_process(crm_current):
        active = process(crm_current['pid'])
    if active is None:
        require(not alive(pid_file('.phone-brain.crm.pid')), 'unidentified CRM process remains alive')
    else:
        require(active['id'] == crm_current['id'], 'CRM process changed outside this deployment')
    if active:
        require(active['cwd'] == crm['cwd'] and active['argv'][1:] == crm['argv'][1:], 'unrecognized CRM process; refusing to stop it')
        require(same_process(active), 'CRM process changed before shutdown')
        set_phase(label + '.crm.stop')
        os.kill(int(active['pid']), signal.SIGTERM)
        set_phase(label + '.crm.wait_exit')
        until = time.monotonic() + 60
        while same_process(active) and time.monotonic() < until:
            if crm_child is not None:
                crm_child.poll()  # Reap a failed replacement; do not confuse a zombie with a live service.
            time.sleep(0.25)
        require(not same_process(active), 'CRM graceful shutdown exceeded 60 seconds; no forced kill issued')
    require(not port_pids(8791), 'CRM listener still exists after shutdown')
    set_phase(label + '.crm.launch')
    with crm_log.open('ab') as log:
        crm_child = subprocess.Popen([str(root / 'bin/crm-linux-amd64'), *crm['argv'][1:]],
                                     cwd=crm['cwd'], env=crm['env'], stdin=subprocess.DEVNULL,
                                     stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
    crm_current = {'pid': str(crm_child.pid), 'id': identity(crm_child.pid)}
    pid_path = root / '.phone-brain.crm.pid'
    temporary = pid_path.with_suffix('.pid.codex')
    temporary.write_text(str(crm_child.pid) + '\n')
    os.replace(temporary, pid_path)
    set_phase(label + '.crm.wait_ready')
    until = time.monotonic() + 20
    while time.monotonic() < until:
        require(crm_child.poll() is None, 'CRM replacement exited; private log remains on the box')
        if not port_pids(8791):
            time.sleep(0.5)
            continue
        try:
            verify_crm(hashes, version)
            set_phase(label + '.crm.ready')
            return
        except (OSError, ValueError):
            time.sleep(0.5)
    raise RuntimeError('CRM replacement did not become healthy')

def verify_journal(hashes):
    active_pid = pid_file('.phone-brain.journal.pid')
    active = process(active_pid)
    require(active_pid in port_pids(int(jaddr.rpartition(':')[2])), 'journal PID is not its listener')
    require(active['cwd'] == journal_proc['cwd'], 'journal working directory changed')
    require(active['argv'][1:] == jargv[1:], 'journal startup arguments changed')
    require(option_from(active['argv'], '-talker') == talker_url and
            option_from(active['argv'], '-talker-playground') == talker_playground,
            'journal Talker endpoints changed')
    require(clean_env(active['env']) == clean_env(journal_proc['env']), 'journal inherited environment changed')
    verify_binary(active, 'bin/journal-linux-amd64', hashes)
    overview = get_json(journal_url)
    require(overview.get('recording') is True and overview.get('target_ms') == 1200 and
            overview.get('retention_hours') == int(ttl) and overview.get('archive') is not None,
            'journal recording, archive, retention, or target is not ready')

def await_journal(hashes):
    # A cold archive may take time to load before the journal listens or serves
    # its first overview. Identity/configuration mismatches remain fatal.
    active_pid = pid_file('.phone-brain.journal.pid')
    require(alive(active_pid), 'journal replacement is not alive')
    started = identity(active_pid)
    until = time.monotonic() + 90
    while time.monotonic() < until:
        require(alive(active_pid) and identity(active_pid) == started, 'journal process exited or changed while becoming ready')
        listeners = port_pids(int(jaddr.rpartition(':')[2]))
        require(not listeners or listeners == {active_pid}, 'journal listener belongs to another process')
        if listeners:
            try:
                verify_journal(hashes)
                return started
            except (OSError, ValueError):
                pass
        time.sleep(1)
    raise RuntimeError('journal did not become ready within 90 seconds')

installed = []
cutover_started = False
recovering = False
try:
    set_phase('candidate.pre_install')
    verify_pinned_quote_policy(root, crm_data)
    verify_archives()
    verify_saved_prompt()
    verify_baseline_files()
    check_protected()
    require(on_port(8765)['id'] == main['id'], 'phone process changed before installation')
    demo_idle()
    if health().get('sessions') != 0:
        raise RuntimeError('a call started before installation')
    set_phase('candidate.install_files')
    for name in files:
        target = root / name
        target.parent.mkdir(parents=True, exist_ok=True)
        temporary = target.with_name(target.name + '.codex-' + TARGET_VERSION)
        shutil.copy2(stage / name, temporary)
        os.replace(temporary, target)
        installed.append(name)
        if name=='configs/telephony.json':
            protected[profile]=CANDIDATE_PROFILE_SHA256
    if health().get('sessions') != 0:
        raise RuntimeError('a call started before restart')
    cutover_started = True
    restart_crm('candidate', candidate_hashes, TARGET_VERSION)
    log_offset = log_path.stat().st_size
    restart('restart-candidate')
    set_phase('candidate.verify_services')
    manifest['health'] = await_version(TARGET_VERSION)
    candidate = on_port(8765)
    verify_runtime(candidate, main)
    verify_binary(candidate, 'bin/golive-linux-amd64', candidate_hashes)
    verify_demo(TARGET_VERSION)
    if demo:
        verify_binary(on_port(demo_port), 'bin/golive-linux-amd64', candidate_hashes)
    verify_crm(candidate_hashes, TARGET_VERSION)
    candidate_journal_id = await_journal(candidate_hashes)
    manifest['state'] = 'running'
    save()
    print(json.dumps({'release': str(release), 'state': 'running', 'health': manifest['health']}), flush=True)
    # Only accept exact startup messages from one stable candidate process.
    # The launcher banner excludes late old-build output before its cutover.
    with log_path.open('rb') as log:
        log.seek(log_offset)
        startup = log.read()
    marker_at = startup.find(('phone-brain: starting golive ' + TARGET_COMMIT + ' ').encode())
    require(marker_at >= 0, 'candidate startup banner not found')
    warm_offset = log_offset + marker_at
    def warmed():
        require(on_port(8765)['id'] == candidate['id'], 'phone service restarted during phrase warming')
        require(health().get('version') == TARGET_VERSION, 'version changed during phrase warming')
        messages = []
        with log_path.open('rb') as log:
            log.seek(warm_offset)
            for raw in log:
                line = raw.decode(errors='replace')
                require(not (line.startswith('--- ') and 'golive exited with status' in line),
                        'phone keepalive restarted the candidate')
                try:
                    messages.append(json.loads(line).get('msg', ''))
                except (ValueError, AttributeError):
                    match = re.search(r'(?:^|\s)msg="([^"\\]*)"(?:\s|$)', line)
                    if match:
                        messages.append(match.group(1))
        require(messages.count('providers registered') == 1, 'candidate startup history is ambiguous')
        require('phrase cache only partly warmed' not in messages and
                "the lead service's openers were not cached" not in messages,
                'phrase warm-up failed; diagnostic log retained on the box')
        return 'phrase cache warmed' in messages and 'openers by scenario cached' in messages
    set_phase('candidate.warming')
    warm_until = time.monotonic() + 310
    while not warmed():
        require(time.monotonic() < warm_until, 'complete phrase and scenario-opener warming not confirmed')
        time.sleep(5)
    set_phase('candidate.verify_final')
    check_protected()
    require(digest(root/'configs/telephony.json')==CANDIDATE_PROFILE_SHA256,'preserved profile differs')
    validate_profile_change((backup/'configs/telephony.json').read_text(),(root/'configs/telephony.json').read_text())
    verify_archives()
    verify_pinned_quote_policy(root,crm_data)
    verify_runtime(on_port(8765), main)
    verify_demo(TARGET_VERSION)
    verify_crm(candidate_hashes, TARGET_VERSION)
    require(await_journal(candidate_hashes) == candidate_journal_id, 'journal restarted during verification')
    verify_untouched()
    require(warmed(), 'warming evidence changed')
    require(all(digest(root/name)==value for name,value in CANDIDATE_FILES.items()),'installed payload changed during deployment')
    manifest['recording_verified'] = True
    manifest['journal_verified'] = True
    manifest['phrase_warm_verified'] = True
    manifest['state'] = 'warmed'
    set_phase('complete')
    print(json.dumps({'state': 'warmed', 'health': health(), 'runtime_profile_preserved': True,
                      'recording_setting': (root / '.phone-brain.record').exists()}), flush=True)
except Exception as error:
    recovering = True
    manifest['failure'] = {'phase': phase, 'type': type(error).__name__}
    if isinstance(error, RuntimeError):
        manifest['failure']['detail'] = str(error)
    persist_diagnostics()
    print('Deployment failed during ' + phase + ': ' + type(error).__name__ +
          (': ' + str(error) if isinstance(error, RuntimeError) else ''), flush=True)
    try:
        set_phase('rollback.restore_files')
        # Do not overwrite a concurrent edit, including the preserved runtime profile.
        for name in installed:
            require(digest(root/name)==digest(stage/name),'rollback blocked by concurrent file edit: '+name)
            if name in existed:
                require(digest(backup/name)==expected[name],'rollback backup differs: '+name)
        for name in installed:
            target = root / name
            if name in existed:
                temporary = target.with_name(target.name + '.codex-rollback')
                shutil.copy2(backup / name, temporary)
                os.replace(temporary, target)
            else:
                target.unlink(missing_ok=True)
        protected.clear();protected.update(baseline_protected)
        verify_saved_prompt()
        verify_archives()
        try:
            h = health()
        except (OSError, ValueError):
            h = {}
        if cutover_started:
            restart_crm('rollback', rollback_hashes, crm_before_version)
            if h.get('status') == 'ok' and h.get('sessions') == 0:
                restart('restart-rollback')
            elif not h and await_stopped():
                restart('restart-rollback', allow_stopped=True)
            else:
                raise RuntimeError('rollback deferred: phone service is busy or not proven stopped')
            h = await_version(before['version'])
            verify_runtime(on_port(8765), main)
            verify_binary(on_port(8765), 'bin/golive-linux-amd64', rollback_hashes)
            verify_demo(before['version'])
            if demo:
                verify_binary(on_port(demo_port), 'bin/golive-linux-amd64', rollback_hashes)
            verify_crm(rollback_hashes, crm_before_version)
            await_journal(rollback_hashes)
        else:
            # No restart was attempted: verify the original processes still
            # serve the reviewed binaries; an active call is left untouched.
            require(all(same_process(proc) for proc in (main, crm, journal_proc)),
                    'a baseline service changed before early rollback verification')
            verify_runtime(on_port(8765), main)
            verify_binary(on_port(8765), 'bin/golive-linux-amd64', rollback_hashes)
            verify_demo(before['version'])
            if demo:
                verify_binary(on_port(demo_port), 'bin/golive-linux-amd64', rollback_hashes)
            verify_crm(rollback_hashes, crm_before_version)
            await_journal(rollback_hashes)
        set_phase('rollback.verify_final')
        verify_baseline_files()
        check_protected()
        manifest['state'] = 'rolled_back' if h.get('status') == 'ok' and h.get('version') == before['version'] else 'rollback_needs_attention'
        manifest['health'] = h
    except Exception as rollback_error:
        manifest['state'] = 'rollback_needs_attention'
        manifest['rollback_failure'] = {'phase': phase, 'type': type(rollback_error).__name__}
        if isinstance(rollback_error, RuntimeError):
            manifest['rollback_failure']['detail'] = str(rollback_error)
        print('Rollback needs attention during ' + phase + ': ' + type(rollback_error).__name__ +
              (': ' + str(rollback_error) if isinstance(rollback_error, RuntimeError) else ''), flush=True)
    persist_diagnostics()
    print(json.dumps({'state': manifest['state'], 'health': manifest.get('health', {})}), flush=True)
    raise SystemExit(1)
PY
