import unittest

class SummaryTests(unittest.TestCase):
    def test_ambiguous_prosody_is_not_scored_as_success(self):
        from summarize_chinese_matrix import classify
        self.assertEqual(classify([{'id':'probe','expected':'ambiguous'}],{})['ambiguous_excluded'],1)
        self.assertEqual(classify([{'id':'probe','expected':'ambiguous'}],{})['floor_cases'],0)

    def test_missed_floor_and_legacy_regression_stay_in_report(self):
        from summarize_chinese_matrix import classify
        rows=[{'id':'f','expected':'floor','floor_crossing_delay_ms':None,
               'floor_within_700ms_without_prior_crossing':False,'pre_onset_floor_crossing':False},
              {'id':'a','expected':'backchannel','any_floor_crossing':True,'backchannel_tail_ge_0_7':True}]
        report=classify(rows,{'a':True})
        self.assertEqual(report['floor_missed_no_crossing'],1)
        self.assertEqual(report['legacy_matched_ack_with_model_floor'],['a'])
        self.assertEqual(report['additional_confident_ack_vs_phrase_list'],[])


if __name__=='__main__':unittest.main()
