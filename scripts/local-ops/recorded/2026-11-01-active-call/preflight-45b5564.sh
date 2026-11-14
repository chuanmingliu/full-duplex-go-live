#timeout 180
#pull /home/chuanming/golive-telephony/codex-latency/preflight-activecall-45b5564-20261101.json preflight-activecall-45b5564-20261101.json
set -eu
python3 -B - <<'PREFLIGHT'
import hashlib,json,os,re,subprocess,urllib.request,time
from pathlib import Path
os.umask(0o077)
root=Path.home()/'golive-telephony'
phase='preflight'
def digest(path):return hashlib.sha256(path.read_bytes()).hexdigest()
def require(ok,message):
    if not ok:raise RuntimeError(message)
def get(url):
    with urllib.request.urlopen(url,timeout=15) as r:return json.load(r)
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


receipt=root/'codex-latency/preflight-activecall-45b5564-20261101.json'
require(not receipt.exists(),'preflight already captured; inspect before retry')
require(get('http://127.0.0.1:8765/healthz')=={'model':'golive-1','sessions':0,'status':'ok','version':'9547d22'},'phone not idle on baseline')
require(get('http://127.0.0.1:9080/healthz').get('sessions')==0,'demo busy')
ports=(8765,9080,8790,8791,8792)
processes={};profiles={}
for port in ports:
    p=on_port(port); base=Path('/proc')/p['pid']
    processes[str(port)]={'pid':p['pid'],'start':p['id'][1],'exe_sha256':digest(base/'exe')}
    if port in (8765,9080):
        profile=resolved(p,option_from(p['argv'],'-profile'));envfile=resolved(p,option_from(p['argv'],'-env'))
        profiles[str(port)]={'profile':str(profile),'profile_sha256':digest(profile),'envfile':str(envfile),'envfile_sha256':digest(envfile),'process_environment_sha256':digest(base/'environ'),'argv_sha256':digest(base/'cmdline')}
        require(p['env'].get('GOLIVE_STORE_TTL_HOURS')=='8760','retention changed')
        if port==8765:require(p['env'].get('GOLIVE_REALTIME_STORE_CALLS')=='1','recording disabled')
files=['CALL-OPERATIONS.md', 'HANDSET-LATENCY.md', 'VERSION', 'bin/crm-linux-amd64', 'bin/golive-linux-amd64', 'bin/journal-linux-amd64', 'scripts/call_latency.py']
hashes={n:digest(root/n) for n in files if (root/n).exists()}
absent=[n for n in files if not (root/n).exists()]
archive_root=Path.home()/'golive-archive';fingerprints={}
for path in sorted((archive_root/'calls').rglob('manifest.json')):
    m=json.loads(path.read_text())
    if not m.get('audio'):continue
    wanted=('audio.wav','transcript.txt','call.json','session.json','golive.log')
    require(all(n in m.get('files',{}) for n in wanted),'complete archive lacks standard files')
    fs={}
    for n in wanted:
        p=path.parent/n;record=m['files'][n]
        require(p.is_file() and not p.is_symlink() and p.stat().st_size==record['bytes'] and digest(p)==record['sha256'],'archive checksum failed')
        fs[n]=record
    require(m['id'] not in fingerprints,'duplicate archive identity')
    fingerprints[m['id']]={'directory':str(path.parent),'manifest_sha256':digest(path),'files':fs}
require(len(fingerprints)>=16,'historical archives missing')
kept=json.loads((Path.home()/'golive-last-call.json').read_text());call=kept['call']
require(call in fingerprints,'last call not archived')
source=Path(fingerprints[call]['directory']);c=json.loads((source/'call.json').read_text());s=json.loads((source/'session.json').read_text())
require(c['id']==call and s['id']==c['session'] and s.get('closed_at'),'saved call identity mismatch')
identity_values={'instructions':s['session'].get('instructions'),'voice':s['session'].get('audio',{}).get('output',{}).get('voice',''),'greeting':c.get('greeting')}
require(all(kept.get(k,'')==v for k,v in identity_values.items()),'saved prompt differs from recorded call')
saved={'call':call,'session':c['session'],**{k+'_sha256':hashlib.sha256(v.encode()).hexdigest() for k,v in identity_values.items()}}
ids=sorted(subprocess.check_output(['docker','ps','-q','--filter','label=com.docker.compose.project=golive-user-simulator'],text=True).split())
require(ids,'simulator unavailable')
simulator=sorted(subprocess.check_output(['docker','inspect','--format','{{.Id}} {{.State.StartedAt}} {{.State.Running}} {{.RestartCount}}',*ids],text=True).splitlines())
all_ids=sorted(subprocess.check_output(['docker','ps','-q'],text=True).split())
containers=sorted(subprocess.check_output(['docker','inspect','--format','{{.Id}} {{.State.StartedAt}} {{.State.Running}} {{.RestartCount}}',*all_ids],text=True).splitlines())
extra_processes={}
for name in ('.phone-brain.edge.pid','.phone-brain.quotesvc.pid'):
    p=process(pid_file(name));extra_processes[name]=list(p['id'])
result={'baseline_commit':(root/'VERSION').read_text().strip(),'hashes':hashes,'absent':absent,'processes':processes,'profiles':profiles,'archive_fingerprints':fingerprints,'calls':[{'call':k} for k in fingerprints],'saved_call_identity':saved,'simulator_identity':simulator,'containers':containers,'extra_processes':extra_processes,'quote_policy':verify_pinned_quote_policy(root)}
require(result['baseline_commit']=='9547d22ecba81e51c1cb338c0c521559886a326d','baseline VERSION changed')
with receipt.open('x') as f:json.dump(result,f,indent=2);f.write('\n')
print(json.dumps({'preflight':str(receipt),'sha256':digest(receipt),'archived_calls':len(fingerprints),'archive_files':5*len(fingerprints),'health':get('http://127.0.0.1:8765/healthz')}))

PREFLIGHT
