import unittest
from pathlib import Path
import tempfile
import workflow
from types import SimpleNamespace

class WorkflowTimelineTests(unittest.TestCase):
    def test_mg_materials_use_original_template_renderer_with_current_content(self):
        with tempfile.TemporaryDirectory() as d:
            folder=Path(d);workflow.write(folder/'p1/timestamps.json',{'sentences':[]})
            state={'input':{'style':'mg-collage','ratio':'16:9'},'segments':[{'id':'p1','text':'先确认声音。再准备素材。','chapter':'制作流程'}]}
            args=SimpleNamespace(task=folder,state=state)
            need={'segment_id':'p1','duration':3,'content':'声音与素材','template':'steps','items':['确认声音','准备素材']}
            manifest=workflow.template_manifest(args,need)
            self.assertEqual(manifest['items'],['确认声音','准备素材'])
            self.assertEqual(manifest['render_mode'],'native-template')
            manifest=workflow.template_manifest(args,{**need,'items':['虚构场景']})
            self.assertNotIn('虚构场景',manifest['items'])
    def test_design_analysis_precedes_shots_and_exposes_renderable_candidates(self):
        with tempfile.TemporaryDirectory() as d:
            folder=Path(d)
            state={'input':{'style':'mg-collage','ratio':'16:9'},'duration':4,'segments':[{'id':'p1','text':'首先确认声音，然后准备素材。','start':0,'duration':4}], 'materials':[{'need_id':'n1','segment_id':'p1','kind':'avatar','path':'materials/n1.mp4','duration':4}], 'shots':[]}
            workflow.write(folder/'timestamps.json',{'total':4,'sentences':[{'i':0,'start':0,'end':4,'text':state['segments'][0]['text'],'words':[]}]})
            candidates=[{'segment_id':'p1','semantic':'步骤','recipes':['host-hero','steps'],'recommended':'steps','source_phrases':['首先确认声音','然后准备素材']}]
            result=workflow.command_plan(SimpleNamespace(task=folder,state=state,inp={'candidates':candidates}))
            self.assertEqual(result['plan']['segments'][0]['recommended'],'steps')
            self.assertIn('步骤',workflow.read(folder/'design-semantics.json')['sentences'][0]['sem'])
            self.assertTrue((folder/'design-card-candidates.json').exists())
            self.assertEqual(state['shots'],[])
    def test_video_sampling_stays_before_last_decodable_frame(self):
        times=workflow.frame_sample_times(0,5.04,25)
        self.assertTrue(all(0<=t<=4.94 for t in times))
        self.assertLessEqual(max(workflow.frame_sample_times(0,5,1)),4)
        self.assertGreaterEqual(min(workflow.frame_sample_times(0,.04,25)),0)
        self.assertTrue(all(20<=t<20.04 for t in workflow.frame_sample_times(20,20.04,25)))
    def test_display_subtitle_correction_keeps_measured_word_timing(self):
        words=[{'text':'配','start':.3,'end':.6},{'text':'音','start':.6,'end':1}]
        row={'text':'配音。','start':.3,'end':1,'words':words}
        result=workflow.subtitle_row(row,'声音！')
        self.assertEqual([w['start'] for w in result['words']],[.3,.6])
        self.assertEqual(result['text'],'声音！')
        with self.assertRaises(ValueError):workflow.subtitle_row(row,'多出一个字')
    def test_audio_chunks_use_actual_word_gaps_and_cover_once(self):
        words=[{'start':0,'end':4.8},{'start':5.1,'end':11.9},{'start':12.3,'end':19.7},{'start':20.1,'end':26}]
        pieces=workflow.piece_ranges(26.5,words,14)
        self.assertEqual(pieces[0][0],0)
        self.assertEqual(pieces[-1][1],26.5)
        for i,(a,b) in enumerate(pieces):
            self.assertGreater(b,a);self.assertLessEqual(b-a,14.01)
            if i:self.assertEqual(a,pieces[i-1][1])
        self.assertTrue(all(not any(w['start']<b<w['end'] for w in words) for _,b in pieces[:-1]))
    def test_cannot_cut_continuous_word_to_fake_coverage(self):
        with self.assertRaises(ValueError):workflow.piece_ranges(20,[{'start':0,'end':20}],14)
    def test_source_mapping_is_distinct_from_global_time(self):
        shots=workflow.default_shots([{'id':'p1','start':0,'duration':4},{'id':'p2','start':4,'duration':5}], [{'need_id':'n1','segment_id':'p1','kind':'avatar'},{'need_id':'n2','segment_id':'p2','kind':'real'}])
        self.assertEqual(shots[1]['start'],4)
        self.assertEqual(shots[-1]['end'],9)
    def test_protected_paths(self):
        with tempfile.TemporaryDirectory() as d:
            with self.assertRaises(ValueError):workflow.safe_file(Path(d),'../escape')
            with self.assertRaises(ValueError):workflow.safe_file(Path(d),'/etc/passwd')
    def test_talkcraft_semantics_and_word_anchor_are_written(self):
        with tempfile.TemporaryDirectory() as d:
            folder=Path(d);state={'input':{'style':'flat-vector','ratio':'9:16'},'duration':2,'segments':[{'id':'p1','text':'确定主题。','start':0,'duration':2}], 'materials':[{'need_id':'n1','kind':'animation','path':'materials/n1.mp4'}],'shots':[{'id':'S1','segment_id':'p1','start':0,'end':2,'primary':'n1','card':'highlighter-sweep','keyword':'确定主题'}]}
            row={'i':0,'start':.2,'end':1.8,'text':'确定主题。','words':[{'text':c,'start':.2+i*.4,'end':.6+i*.4} for i,c in enumerate('确定主题')]}
            workflow.write(folder/'timestamps.json',{'total':2,'sentences':[row]})
            workflow.write(folder/'timing.json',workflow.load_upstream('make_timing').make_timing_data({'total':2,'sentences':[row]}))
            workflow.command_shotbook(SimpleNamespace(task=folder,state=state))
            self.assertEqual(len(workflow.read(folder/'semantics.json')['sentences']),1)
            self.assertAlmostEqual(workflow.read(folder/'beats.json')[0]['t'],.2)
            self.assertEqual(workflow.read(folder/'shots.json')[0]['anchor_frame'],5)
            book=(folder/'SHOTBOOK.md').read_text()
            self.assertIn('纯动效(materials/n1.mp4)',book)
            self.assertNotIn('V(materials/n1.mp4)',book)
            self.assertIn('## 未完成 / 未采集清单',book)
            self.assertIn('人物形态',book)
            self.assertIn('配音', (folder/'sources.md').read_text())
            self.assertIn('1 张卡', (folder/'preflight.log').read_text())

if __name__=='__main__':unittest.main()
