import unittest
from media_quality import motion_window, audio_metrics, quality_issues

class MediaQualityTests(unittest.TestCase):
 def test_split_material_preserves_its_narration_offset(self):
  needs=[{'id':'a','kind':'animation','segment_id':'p1','duration':11},{'id':'b','kind':'animation','segment_id':'p1','duration':8.5}]
  self.assertEqual(motion_window(needs,needs[1],19.5),{'start':11,'end':19.5})
  explicit={**needs[1],'narration_start':13.5,'duration':6}
  self.assertEqual(motion_window(needs,explicit,19.5),{'start':13.5,'end':19.5})
 def test_out_of_range_is_not_silently_truncated(self):
  with self.assertRaises(ValueError):motion_window([] ,{'duration':8,'narration_start':13.5},19.5)
 def test_quality_status_does_not_treat_a_decodable_file_as_reviewed(self):
  state={'shots':[{'id':'s1','start':0,'end':.8,'segment_id':'p1'}],'segments':[]}
  issues=quality_issues(state,[])
  self.assertTrue(any(x['code']=='short_shot' and x['time']==0 for x in issues))

if __name__=='__main__':unittest.main()
