import unittest
from avatar_framing import select_primary_face, summarize_track

def face(x,y,w,h,confidence=.95):
    return [x,y,w,h]+[0]*10+[confidence]

class AvatarFramingTests(unittest.TestCase):
    def test_main_person_wins_over_high_confidence_inset(self):
        main=face(230,160,240,330,.87)
        inset=face(550,950,85,110,.99)
        self.assertEqual(select_primary_face([inset,main],720,1280)[0][:4],main[:4])

    def test_similar_people_are_ambiguous(self):
        _,ambiguous=select_primary_face([face(80,100,210,300),face(400,100,205,295)],720,1280)
        self.assertTrue(ambiguous)

    def test_overlapping_detections_of_same_face_do_not_trigger_multi_person_fallback(self):
        correct=face(90,450,489,552,.81)
        duplicate=face(53,152,564,567,.655)
        selected,ambiguous=select_primary_face([correct,duplicate],720,1280)
        self.assertFalse(ambiguous)
        self.assertEqual(selected,correct)

    def test_zoom_and_unreliable_evidence_are_reported(self):
        rows=[{'t':0,'face':[220,200,200,260],'score':.9},{'t':1,'face':[120,100,400,520],'score':.9}]
        report=summarize_track(rows,720,1280,2,2)
        self.assertIn('source_zoom',report['warnings'])
        self.assertEqual(report['status'],'tracked')
        self.assertEqual(summarize_track(rows,720,1280,2,10)['status'],'uncertain')
        self.assertEqual(summarize_track([],720,1280,2,4)['safe_zone'],[])

    def test_cut_is_kept_instead_of_smearing_two_piece_positions(self):
        rows=[{'t':0,'face':[80,120,180,240],'score':.9},{'t':1,'face':[440,120,180,240],'score':.9}]
        report=summarize_track(rows,720,1280,2,2)
        self.assertTrue(report['samples'][1]['cut'])

if __name__=='__main__':unittest.main()
