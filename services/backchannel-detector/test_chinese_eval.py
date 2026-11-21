import unittest
import numpy as np
from chinese_eval import alaw_roundtrip, onset_end, summarize_frames


class EvaluationTests(unittest.TestCase):
    def test_uses_chunk_availability_not_half_chunk_future(self):
        rows=[dict(frame_end_ms=i*20,available_ms=((i+1)//2)*40,p_floor=.8,p_backchannel=.1)
              for i in range(1,5)]
        r=summarize_frames(rows,0,80,'floor')
        self.assertEqual(r['floor_crossing_delay_ms'],40)
        self.assertEqual(r['floor_three_frame_delay_ms'],80)

    def test_separated_crossings_are_not_sustained(self):
        rows=[dict(frame_end_ms=i*20,available_ms=((i+1)//2)*40,p_floor=p,p_backchannel=.1)
              for i,p in enumerate([.8,.8,.1,.8,.8],1)]
        self.assertIsNone(summarize_frames(rows,0,100,'floor')['floor_three_frame_delay_ms'])

    def test_codec_keeps_rate_duration_and_silence(self):
        a=np.zeros(16000,dtype=np.int16)
        b=alaw_roundtrip(a)
        self.assertEqual(len(a),len(b))
        self.assertLess(int(abs(b).max()),16)
        self.assertEqual(onset_end(b),(None,None))
        a[320:640]=3000
        self.assertEqual(onset_end(a),(320,640))

    def test_late_detection_is_not_within_700ms(self):
        rows=[dict(frame_end_ms=720,available_ms=720,p_floor=.8,p_backchannel=.1)]
        self.assertFalse(summarize_frames(rows,0,100,'floor')['floor_within_700ms'])


if __name__=='__main__':unittest.main()
