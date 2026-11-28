"""Predeclared Chinese benefit/regression matrix; no production control or dialing."""
import argparse
import asyncio
import hashlib
import json
from pathlib import Path

import aiohttp
import numpy as np
from chinese_eval import RATE, alaw_roundtrip, onset_end, read_wav, replay, summarize_frames
from chinese_archive_eval import evaluate_archive


VARIANTS = ('pcm16', 'g711_alaw', 'noise_snr10', 'g711_echo_minus18db')


def pcm(value):
    return np.clip(np.rint(value), -32768, 32767).astype(np.int16)


def transform(user, bot, variant, seed):
    if variant == 'pcm16':
        return user.copy()
    if variant == 'g711_alaw':
        return alaw_roundtrip(user)
    if variant == 'noise_snr10':
        voiced = user[np.abs(user.astype(float)) >= 300].astype(float)
        rms = float(np.sqrt(np.mean(voiced**2))) if len(voiced) else 1000
        noise = np.random.default_rng(seed).normal(0, rms/np.sqrt(10), len(user))
        return pcm(user.astype(float)+noise)
    if variant == 'g711_echo_minus18db':
        delay = RATE*80//1000
        echo = np.pad(bot, (delay, 0))[:len(user)].astype(float)*10**(-18/20)
        return alaw_roundtrip(pcm(user.astype(float)+echo))
    raise ValueError('unknown_variant')


def summarize(rows, onset, end, expected):
    result = summarize_frames(rows, onset, end, expected)
    before = [r for r in rows if r['available_ms'] < onset]
    result['pre_onset_floor_crossing'] = any(r['p_floor'] >= .7 for r in before)
    result['floor_within_700ms_without_prior_crossing'] = (
        result['floor_within_700ms'] and not result['pre_onset_floor_crossing'])
    tail = result['mean_backchannel_first_200ms_after_end']
    result['backchannel_tail_ge_0_7'] = tail is not None and tail >= .7
    result['any_floor_crossing'] = result['pre_onset_floor_crossing'] or result['floor_crossing_delay_ms'] is not None
    return result


def sequence_windows(rows, ack_start, ack_end, floor_start, floor_end):
    # Future interruption samples must not turn an earlier acknowledgment into
    # an apparent error; account for full 40 ms chunk availability.
    before_floor = [r for r in rows if ack_start <= r['available_ms'] <= floor_start]
    before_summary = summarize_frames(before_floor, ack_start, ack_end, 'backchannel')
    floor_summary = summarize(rows, floor_start, floor_end, 'floor')
    return {'premature_floor_before_second_phrase': any(r['p_floor'] >= .7 for r in before_floor),
            'ack_phase_peak_floor': before_summary['peak_floor_during_speech'],
            'second_phrase': floor_summary}


async def evaluate(args):
    manifest_path = Path(__file__).with_name('chinese_matrix.json')
    manifest = json.loads(manifest_path.read_text())
    assert manifest['threshold'] == .7 and manifest['persistence_frames'] == 3
    cases = manifest['cases']
    clips = sorted(Path(args.clips).glob('spoken_*.wav'))
    assert len(clips) == len(cases)
    mono = {c['id']:read_wav(p, 1)[:,0] for c,p in zip(cases,clips)}
    reference = mono['floor_wait']
    start,end = onset_end(reference)
    reference = reference[start:end]
    results = {'protocol_sha256':hashlib.sha256(manifest_path.read_bytes()).hexdigest(),
               'model_revision':None, 'controller':'legacy', 'live_actions':0,
               'threshold_tuned':False, 'synthetic_rows':[], 'sequence_rows':[],
               'control_rows':[], 'paced_sentinels':[], 'archives':[]}
    out = Path(args.output)
    def save():
        temporary = out.with_suffix('.tmp')
        temporary.write_text(json.dumps(results,indent=2)+'\n')
        temporary.replace(out)
    async with aiohttp.ClientSession(timeout=aiohttp.ClientTimeout(total=1800,sock_read=5)) as client:
        async with client.get(args.url+'/healthz') as response:
            results['service_before'] = await response.json()
        results['model_revision'] = results['service_before']['model_revision']
        for index,(case,path) in enumerate(zip(cases,clips)):
            original = mono[case['id']]
            start,end = onset_end(original)
            assert start is not None, 'silent_fixture'
            user = np.pad(original,(RATE*2,RATE*2))
            bot = np.resize(reference,len(user))
            for variant in VARIANTS:
                modified = transform(user,bot,variant,manifest['noise_seed']+index)
                rows,cost = await replay(client,args.url,np.column_stack((modified,bot)),continuous_bot=True)
                results['synthetic_rows'].append({
                    'id':case['id'],'group':case['group'],'split':case['split'],'variant':variant,
                    'fixture_sha256':hashlib.sha256(path.read_bytes()).hexdigest(),
                    **summarize(rows,2000+start//16,2000+end//16,case['expected']),
                    'roundtrip':cost})
            save()
        # Paired sequences test an ack prefix then genuine floor-taking after
        # different pauses. The second onset is measured from its own clip.
        ack, floor = mono['ack_mm'], mono['floor_wait']
        ack_start,ack_end = onset_end(ack)
        floor_onset,floor_end = onset_end(floor)
        for gap in manifest['sequence_gaps_ms']:
            second_offset = RATE*2+len(ack)+gap*16
            source = np.concatenate((np.zeros(RATE*2,np.int16),ack,np.zeros(gap*16,np.int16),floor,np.zeros(RATE*2,np.int16)))
            bot = np.resize(reference,len(source))
            for variant in VARIANTS[:2]:
                user = transform(source,bot,variant,0)
                rows,cost = await replay(client,args.url,np.column_stack((user,bot)),continuous_bot=True)
                results['sequence_rows'].append({'gap_ms':gap,'variant':variant,
                    **sequence_windows(rows,2000+ack_start//16,2000+ack_end//16,
                                       (second_offset+floor_onset)//16,(second_offset+floor_end)//16),'roundtrip':cost})
        # Identity-negative controls intentionally use speech capable of
        # sounding like an interruption; caller identity is an independent gate.
        length = RATE*6
        bot = np.resize(reference,length)
        seed = manifest['noise_seed']
        controls = {
            'silence':np.zeros(length,np.int16),
            'noise_only':pcm(np.random.default_rng(seed).normal(0,300,length)),
            'delayed_echo_only':pcm(np.pad(bot,(1280,0))[:length].astype(float)*10**(-18/20)),
            'bystander_speech_only':pcm(np.pad(mono['floor_correction'],(RATE, length))[:length].astype(float)*.178)}
        for ident,user in controls.items():
            rows,cost = await replay(client,args.url,np.column_stack((user,bot)),continuous_bot=True)
            results['control_rows'].append({'id':ident,**summarize(rows,0,6000,'no_cut_control'),'roundtrip':cost})
        # Repeat a predeclared subset at wall-clock cadence to check that fast
        # offline replay is not hiding streaming costs or changing predictions.
        for index,case in enumerate(cases[:12]):
            original=mono[case['id']];start,end=onset_end(original)
            user=np.pad(original,(RATE*2,RATE*2));bot=np.resize(reference,len(user))
            rows,cost=await replay(client,args.url,np.column_stack((alaw_roundtrip(user),bot)),continuous_bot=True,paced=True)
            results['paced_sentinels'].append({'id':case['id'],'variant':'g711_alaw',
                **summarize(rows,2000+start//16,2000+end//16,case['expected']),'roundtrip':cost})
            save()
        for archive in args.archive:
            results['archives'].append(await evaluate_archive(client,args.url,archive,out.parent))
            save()
        async with client.get(args.url+'/healthz') as response:
            results['service_after']=await response.json()
    save()
    print(json.dumps({'completed':True,'matrix_rows':len(results['synthetic_rows']),
                      'sequences':len(results['sequence_rows']),'controls':len(results['control_rows']),
                      'paced_sentinels':len(results['paced_sentinels']),'archives':len(results['archives'])}))


if __name__=='__main__':
    parser=argparse.ArgumentParser()
    parser.add_argument('--url',default='http://127.0.0.1:18087')
    parser.add_argument('--clips',required=True)
    parser.add_argument('--output',required=True)
    parser.add_argument('--archive',action='append',default=[])
    asyncio.run(evaluate(parser.parse_args()))
