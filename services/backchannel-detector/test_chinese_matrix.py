import unittest
import numpy as np
from chinese_matrix_eval import pcm, transform, summarize, sequence_windows
from chinese_archive_eval import correlate


def frame(end, floor=.1):
    return {'available_ms':end,'frame_end_ms':end,'p_floor':floor,'p_backchannel':.8,'vad':.8}


class MatrixTests(unittest.TestCase):
    def test_noise_is_reproducible_and_cannot_wrap_int16(self):
        user=np.full(16000,32760,dtype=np.int16);bot=user.copy()
        self.assertTrue(np.array_equal(transform(user,bot,'noise_snr10',7),transform(user,bot,'noise_snr10',7)))
        self.assertTrue(np.array_equal(pcm(np.array([40000,-40000])),[32767,-32768]))
        self.assertFalse(np.array_equal(transform(user,bot,'noise_snr10',7),transform(user,bot,'noise_snr10',8)))

    def test_early_echo_crossing_is_not_credited_as_success(self):
        r=summarize([frame(80,.9),frame(2040,.9)],2000,2200,'floor')
        self.assertTrue(r['floor_within_700ms'])
        self.assertFalse(r['floor_within_700ms_without_prior_crossing'])
        self.assertTrue(r['pre_onset_floor_crossing'])

    def test_second_phrase_does_not_relabel_first_ack(self):
        r=sequence_windows([frame(40),frame(80),frame(120,.9),frame(160,.9)],0,60,100,180)
        self.assertFalse(r['premature_floor_before_second_phrase'])
        self.assertEqual(r['second_phrase']['floor_crossing_delay_ms'],20)

    def test_ack_prefix_can_itself_false_trigger(self):
        r=sequence_windows([frame(40,.9),frame(120,.9)],0,60,100,180)
        self.assertTrue(r['premature_floor_before_second_phrase'])
        self.assertFalse(r['second_phrase']['floor_within_700ms_without_prior_crossing'])

    def test_missing_model_signal_is_retained(self):
        r=summarize([frame(40)],0,80,'floor')
        self.assertIsNone(r['floor_crossing_delay_ms'])
        self.assertFalse(r['floor_within_700ms'])

    def test_correlation_counts_unpaired_and_preserves_kept_examples(self):
        origin='2026-11-02T12:00:00+00:00'
        def ev(at,msg,**fields):
            return {'time':f'2026-11-02T12:00:00.{at:03d}+00:00','msg':msg,**fields}
        cut='new query while answering: cutting the answer'
        events=[ev(10,cut,barge_in=True,speaking=True),
                ev(20,'listen: utterance opened',barge_in=True),ev(100,cut,barge_in=True,speaking=True),
                ev(200,'listen: utterance opened',barge_in=True),
                ev(300,'listen: backchannel, not an interruption; keeping the floor')]
        result=correlate(events,[frame(40,.9),frame(60,.9),frame(80,.9),frame(220)],origin)
        self.assertEqual(result['unpaired_cuts'],1)
        self.assertEqual(result['cuts'][0]['raw_signal_lead_ms'],60)
        self.assertEqual(result['cuts'][0]['sustained_floor_after_open_ms'],60)
        self.assertIsNone(result['kept_backchannels'][0]['first_floor_after_open_ms'])


if __name__=='__main__':unittest.main()
