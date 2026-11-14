#timeout 180
#pull /home/chuanming/golive-telephony/codex-latency/reconciled-activecall-45b5564-20261101.json reconciled-activecall-45b5564-20261101.json
set -eu
python3 -B - <<'RECONCILE'
import fcntl,hashlib,json,os,re,shutil,subprocess,urllib.request
from pathlib import Path
os.umask(0o077)
root=Path.home()/'golive-telephony';release=Path.home()/'golive-releases/codex-45b5564-20261101-162734'
def require(ok,msg):
    if not ok:raise RuntimeError(msg)
def digest(p):return hashlib.sha256(p.read_bytes()).hexdigest()
def get(url):
    with urllib.request.urlopen(url,timeout=10) as r:return json.load(r)
def ident(pid):return str(pid),(Path('/proc')/str(pid)/'stat').read_text().rsplit(')',1)[1].split()[19]
def listener(port):
    ids=set(re.findall(r'pid=(\d+)',subprocess.check_output(['ss','-lntpH',f'sport = :{port}'],text=True)))
    require(len(ids)==1,'listener ownership changed');return ids.pop()
lock=(root/'.codex-deploy.lock').open('r');fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
prefpath=root/'codex-latency/preflight-activecall-45b5564-20261101.json'
require(digest(prefpath)=='76c6798208f5afa46c77ec8129932293a1b9c3c6aeca1eab801eec5a1f51e90e','preflight changed')
pref=json.loads(prefpath.read_text())
failedpath=root/'codex-latency/deployment-activecall-45b5564-20261101.json';failed=json.loads(failedpath.read_text())
require(failed['state']=='rollback_needs_attention' and failed['failure']['detail']=='saved context archive changed' and failed['rollback_failure']['detail']=='saved context archive changed','different failed attempt')
out=root/'codex-latency/reconciled-activecall-45b5564-20261101.json'
require(not out.exists(),'reconciliation already attempted; inspect instead of repeating')
CANDIDATE_FILES={'CALL-OPERATIONS.md': '93d6f931bcaaca65549dced0880151beeb670bdfe42af521a501be2373c44d1a', 'HANDSET-LATENCY.md': 'f7d751626c843cb33a78759c736e64c2ef987c4830f11a854543d8edeb1aaf75', 'VERSION': '4e88b7909446364ea5427e1050c41151f3ec5f660705f190746e988c5bbc4fc0', 'bin/crm-linux-amd64': '863d264d8914317708da62e99135f0791323ce9c266c6159dddc5cad0e5a08d2', 'bin/golive-linux-amd64': '0af8a624053a706bc4f2f20ec2b9f847409eef2e6f502e21520cca60a8ec8a29', 'bin/journal-linux-amd64': '4b098f57749623de459dd99cd3abb9f0c72e367fec6432eac30bbb7a05231cc1', 'scripts/call_latency.py': 'c5df3bf2a120d4bcc241f75a1e2c28b3205a903dd94b33516fa21e11ea549a50'}
OWNERS={8765:('1287666','183822890'),9080:('1287825','183823503'),8791:('1287488','183822805'),8790:('1287800','183823397')}
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


def verify_services():
    for port,expected in OWNERS.items():
        pid=listener(port);require(ident(pid)==expected,'service restarted during reconciliation')
        name='golive' if port in (8765,9080) else ('crm' if port==8791 else 'journal')
        require(digest(Path('/proc')/pid/'exe')==CANDIDATE_FILES['bin/'+name+'-linux-amd64'],'running candidate bytes changed')
    for port in (8765,9080):
        h=get(f'http://127.0.0.1:{port}/healthz')
        require(h.get('status')=='ok' and h.get('sessions')==0 and h.get('version')=='45b5564','engine is busy or changed')
        base=Path('/proc')/OWNERS[port][0]; record=pref['profiles'][str(port)]
        require(digest(base/'cmdline')==record['argv_sha256'],'engine arguments changed')
        for pathkey,hashkey in (('profile','profile_sha256'),('envfile','envfile_sha256')):
            require(digest(Path(record[pathkey]))==record[hashkey],'runtime config changed')
    env=dict(os.fsdecode(v).split('=',1) for v in (Path('/proc')/OWNERS[8765][0]/'environ').read_bytes().split(b'\0') if b'=' in v)
    require(env.get('GOLIVE_STORE_TTL_HOURS')=='8760' and env.get('GOLIVE_REALTIME_STORE_CALLS')=='1' and env.get('GOLIVE_STORE_DIR')==str(Path.home()/'golive-calls'),'recording settings changed')
    overview=get('http://192.168.71.9:8790/api/overview')
    require(overview.get('recording') is True and overview.get('retention_hours')==8760 and overview.get('target_ms')==1200,'journal settings changed')
    require(get('http://127.0.0.1:8791/healthz').get('status')=='ok','CRM unhealthy')
    require(ident(listener(8792))==(pref['processes']['8792']['pid'],pref['processes']['8792']['start']),'Desk changed')
    for name,identity in pref['extra_processes'].items():
        pid=(root/name).read_text().strip();require(ident(pid)==tuple(identity),'independent service changed')
    ids=sorted(subprocess.check_output(['docker','ps','-q'],text=True).split())
    rows=sorted(subprocess.check_output(['docker','inspect','--format','{{.Id}} {{.State.StartedAt}} {{.State.Running}} {{.RestartCount}}',*ids],text=True).splitlines())
    require(rows==pref['containers'],'container state changed')
    require(digest(root/'scripts/phone-brain.sh')=='c21b7fa28d483ec18fb7c2a6eb714c08b6bf40358e48c4235b034d7b441d482a','launcher changed')
    saved=json.loads((Path.home()/'golive-last-call.json').read_text()); expected=pref['saved_call_identity']
    require(saved['call']==expected['call'],'new call appeared')
    for key in ('instructions','voice','greeting'):
        require(hashlib.sha256(saved.get(key,'').encode()).hexdigest()==expected[key+'_sha256'],'saved context changed')
    verify_pinned_quote_policy(root)
    text=(root/'golive-telephony.log').read_text(errors='replace')
    marker='phone-brain: starting golive 45b55649290007c275eb682cbab4d40ea4380fcf'
    require(text.count(marker)==1,'candidate restarted or startup missing')
    startup=text.split(marker,1)[1]
    require('golive exited with status' not in startup and 'phrase cache only partly warmed' not in startup,'candidate restart/warm failure')
    require(startup.count('phrase cache warmed')==1 and startup.count('openers by scenario cached')==1,'complete warmup not proven')

def verify_archives():
    audit=[]
    for cid,record in pref['archive_fingerprints'].items():
        directory=Path(record['directory']);manifest=json.loads((directory/'manifest.json').read_text())
        require(manifest['id']==cid and manifest.get('audio'),'archive manifest identity changed')
        for name,stamp in record['files'].items():
            path=directory/name;require(path.is_file() and not path.is_symlink(),'archive file missing or linked')
            raw=path.read_bytes();now=hashlib.sha256(raw).hexdigest()
            require(manifest['files'][name]['sha256']==now and manifest['files'][name]['bytes']==len(raw),'manifest no longer matches file')
            if name!='call.json':
                require(now==stamp['sha256'] and len(raw)==stamp['bytes'],'raw recording/transcript/session/log changed')
                continue
            original,n=re.subn(rb'    "conversational_first_content": \{\n(?:      [^\n]*\n)*    \},\n    "quote_only_responses": [0-9]+,\n',b'',raw)
            require(n==1 and hashlib.sha256(original).hexdigest()==stamp['sha256'] and len(original)==stamp['bytes'],'archive changed beyond the two reviewed additive latency fields')
            audit.append({'call':cid,'before_sha256':stamp['sha256'],'after_sha256':now,'original_bytes_reconstructed':True})
    require(len(audit)==143,'archive inventory changed')
    return audit

verify_services();audit=verify_archives()
for name,wanted in CANDIDATE_FILES.items():
    require(digest(release/'candidate'/name)==wanted,'candidate payload changed')
    require(digest(release/'previous'/name)==pref['hashes'][name],'rollback copy changed')
    require(digest(root/name)==pref['hashes'][name],'on-disk files changed since failed rollback')
state={'candidate':'45b5564','state':'reconciling','phase':'install_matching_disk_files','failed_receipt_sha256':digest(failedpath),'release':str(release),'pid_identity':{str(k):list(v) for k,v in OWNERS.items()},'archive_metadata_audit':audit,'raw_archive_files_unchanged':572,'archive_files_verified':715,'archived_calls_verified':143,'ci':{'commit': '45b55649290007c275eb682cbab4d40ea4380fcf', 'conclusion': 'success', 'run_id': 41287492177, 'url': 'https://github.com/chuanmingliu/full-duplex-go-live/actions/runs/41287492177', 'jobs': [{'name': 'Go tests (normal)', 'status': 'completed', 'conclusion': 'success'}, {'name': 'Go tests (race)', 'status': 'completed', 'conclusion': 'success'}, {'name': 'quality', 'status': 'completed', 'conclusion': 'success'}]}}
with out.open('x') as f:json.dump(state,f,indent=2);f.write('\n')
for name in CANDIDATE_FILES:
    target=root/name;temp=target.with_name(target.name+'.reconcile-45b5564');require(not temp.exists(),'staging collision')
    shutil.copy2(release/'candidate'/name,temp);os.replace(temp,target)
verify_services();require(verify_archives()==audit,'archive changed during reconciliation')
require(all(digest(root/n)==h for n,h in CANDIDATE_FILES.items()),'installed files differ')
state.update(state='warmed',phase='complete',recording_verified=True,journal_verified=True,phrase_warm_verified=True,runtime_configs_preserved=True,existing_services_preserved=True)
tmp=out.with_suffix('.tmp');tmp.write_text(json.dumps(state,indent=2)+'\n');os.replace(tmp,out)
print(json.dumps({'state':state['state'],'phase':state['phase'],'candidate':'45b5564','raw_archive_files_unchanged':572,'additive_call_metadata_verified':143,'services_restarted':False,'receipt':str(out)}))

RECONCILE
