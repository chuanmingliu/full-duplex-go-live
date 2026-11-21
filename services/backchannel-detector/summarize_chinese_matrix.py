"""Summarize all matrix outcomes without filtering by whether the model wins."""
import argparse
import json
import math
from pathlib import Path
import statistics


def distribution(values):
    if not values:
        return {'n':0,'median_ms':None,'p95_ms':None,'max_ms':None}
    values=sorted(values)
    return {'n':len(values),'median_ms':statistics.median(values),
            'p95_ms':values[math.ceil(len(values)*.95)-1],'max_ms':max(values)}


def classify(rows, legacy):
    floor=[r for r in rows if r['expected']=='floor']
    ack=[r for r in rows if r['expected']=='backchannel']
    return {'rows':len(rows),'floor_cases':len(floor),'ack_cases':len(ack),
            'ambiguous_excluded':sum(r['expected']=='ambiguous' for r in rows),
            'floor_within_700ms_no_precross':sum(r['floor_within_700ms_without_prior_crossing'] for r in floor),
            'floor_missed_no_crossing':sum(r['floor_crossing_delay_ms'] is None for r in floor),
            'floor_with_precross':sum(r['pre_onset_floor_crossing'] for r in floor),
            'floor_delays_all_detected':distribution([r['floor_crossing_delay_ms'] for r in floor if r['floor_crossing_delay_ms'] is not None]),
            'ack_any_floor_crossing':sum(r['any_floor_crossing'] for r in ack),
            'ack_confident_tail_without_floor':sum(r['backchannel_tail_ge_0_7'] and not r['any_floor_crossing'] for r in ack),
            'additional_confident_ack_vs_phrase_list':[r['id'] for r in ack if not legacy[r['id']] and r['backchannel_tail_ge_0_7'] and not r['any_floor_crossing']],
            'legacy_matched_ack_with_model_floor':[r['id'] for r in ack if legacy[r['id']] and r['any_floor_crossing']]}


def summarize(result, baseline):
    legacy={r['id']:r['legacy_phrase_match'] for r in baseline['rows']}
    rows=result['synthetic_rows']
    cuts=[r for a in result['archives'] for r in a['cuts']]
    keeps=[r for a in result['archives'] for r in a['kept_backchannels']]
    signals=[r for r in cuts if r['raw_signal_lead_ms'] is not None]
    sustained=[r for r in cuts if r['sustained_floor_after_open_ms'] is not None]
    stats={'protocol_sha256':result['protocol_sha256'],'total_synthetic_trials':len(rows)+len(result['sequence_rows'])+len(result['control_rows'])+len(result['paced_sentinels']),
           'variants':{variant:classify([r for r in rows if r['variant']==variant],legacy) for variant in sorted({r['variant'] for r in rows})},
           'new_case_groups_pcm':{group:classify([r for r in rows if r['variant']=='pcm16' and r['group']==group],legacy) for group in sorted({r['group'] for r in rows if r['split']=='new_cases'})},
           'controls':[{k:r[k] for k in ('id','any_floor_crossing','floor_crossing_delay_ms')} for r in result['control_rows']],
           'sequences':{'n':len(result['sequence_rows']),'premature_cuts':sum(r['premature_floor_before_second_phrase'] for r in result['sequence_rows']),
                        'second_floor_within_700ms_no_precross':sum(r['second_phrase']['floor_within_700ms_without_prior_crossing'] for r in result['sequence_rows']),
                        'second_phrase_delays':distribution([r['second_phrase']['floor_crossing_delay_ms'] for r in result['sequence_rows'] if r['second_phrase']['floor_crossing_delay_ms'] is not None])},
           'archive_comparison':{'archives':len(result['archives']),'audio_seconds':sum(a['audio_seconds'] for a in result['archives']),
                                 'legacy_cut_events':len(cuts),'raw_signal_available_before_cut':len(signals),
                                 'sustained_signal_available_before_cut':len(sustained),
                                 'raw_signal_missing_before_cut':len(cuts)-len(signals),
                                 'lead_over_existing_cut_detected_subset':distribution([r['raw_signal_lead_ms'] for r in signals]),
                                 'legacy_kept_backchannels':len(keeps),'kept_backchannels_with_model_floor_crossing':sum(r['first_floor_after_open_ms'] is not None for r in keeps),
                                 'unpaired_cuts':sum(a['unpaired_cuts'] for a in result['archives'])},
           'live_control_actions':0,'handset_latency_measured':False}
    matched={r['id']:r for r in rows if r['variant']=='g711_alaw' and r['split']=='regression'}
    fields=('floor_crossing_delay_ms','floor_three_frame_delay_ms','pre_onset_floor_crossing','backchannel_tail_ge_0_7')
    stats['paced_sentinels']={'n':len(result['paced_sentinels']),
        'same_classification_and_audio_timing':sum(all(r[k]==matched[r['id']][k] for k in fields) for r in result['paced_sentinels']),
        'roundtrip_p95_range_ms':[min(r['roundtrip']['p95_ms'] for r in result['paced_sentinels']),max(r['roundtrip']['p95_ms'] for r in result['paced_sentinels'])]}
    return stats


if __name__=='__main__':
    p=argparse.ArgumentParser()
    p.add_argument('results');p.add_argument('baseline');p.add_argument('--output',required=True)
    args=p.parse_args()
    result=summarize(json.loads(Path(args.results).read_text()),json.loads(Path(args.baseline).read_text()))
    Path(args.output).write_text(json.dumps(result,indent=2)+'\n')
    print(json.dumps(result,indent=2))
