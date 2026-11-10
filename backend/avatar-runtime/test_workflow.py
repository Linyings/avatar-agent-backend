import unittest
from pathlib import Path
import tempfile
from unittest.mock import patch
import workflow
from types import SimpleNamespace

class WorkflowTimelineTests(unittest.TestCase):
    def test_portrait_review_rejects_files_outside_workflow_storage(self):
        with tempfile.TemporaryDirectory() as d:
            task=Path(d)/'workflow-v2'/'runs'/'r1';task.mkdir(parents=True)
            args=SimpleNamespace(task=task,inp={'source':str(Path(d)/'outside.jpg')})
            with self.assertRaisesRegex(ValueError,'outside workflow storage'):workflow.command_portrait_frames(args)

    def test_portrait_review_samples_bounded_frames_and_does_not_claim_lipsync(self):
        with tempfile.TemporaryDirectory() as d:
            task=Path(d)/'workflow-v2'/'runs'/'r1';task.mkdir(parents=True)
            source=task/'clip.mp4';source.write_bytes(b'fixture')
            args=SimpleNamespace(task=task,inp={'source':str(source),'video':True},ffmpeg='ffmpeg')
            calls=[]
            def encode(argv,**kw):calls.append(argv);Path(argv[-1]).write_bytes(b'image')
            with patch.object(workflow,'probe',return_value={'streams':[{'codec_type':'video','duration':'7.2'}]}),patch.object(workflow,'run',side_effect=encode):
                result=workflow.command_portrait_frames(args)
            self.assertEqual(len(result['frames']),8)
            self.assertEqual(result['lip_sync'],'not_measured')
            self.assertLess(max(result['times']),7.2)

    def test_avatar_material_uses_full_source_track_and_discards_legacy_last_piece_box(self):
        with tempfile.TemporaryDirectory() as d:
            folder=Path(d);(folder/'host.mp4').write_bytes(b'fixture')
            material={'need_id':'host','kind':'avatar','path':'host.mp4','face_zone':[500,900,100,150]}
            track={'status':'tracked','safe_zone':[180,70,350,600],'warnings':[]}
            args=SimpleNamespace(task=folder,ffprobe='ffprobe')
            with patch.object(workflow,'probe',return_value={'streams':[{'codec_type':'video','width':720,'height':1280}]}),patch.object(workflow,'analyze_avatar',return_value=track) as analyze:
                enriched,_=workflow.enrich_avatar_material(args,material,[11])
                self.assertEqual(enriched['face_zone'],track['safe_zone'])
                self.assertEqual(enriched['avatar_framing'],track)
                self.assertEqual(analyze.call_args.args[2],[11])
            self.assertEqual(material['face_zone'],[500,900,100,150])
            with patch.object(workflow,'probe',return_value={'streams':[{'codec_type':'video','width':720,'height':1280}]}),patch.object(workflow,'analyze_avatar',side_effect=RuntimeError('unavailable')):
                enriched,warnings=workflow.enrich_avatar_material(args,material)
                self.assertEqual(enriched['face_zone'],[])
                self.assertNotIn('avatar_framing',enriched)
                self.assertTrue(warnings)

    def _avatar_args(self,folder,lip_sync,score):
        (folder/'pieces').mkdir();(folder/'pieces'/'n1-c1.mp4').write_bytes(b'fixture');(folder/'pieces'/'n1-c1.wav').write_bytes(b'fixture')
        need={'id':'n1','segment_id':'p1','kind':'avatar','duration':2.6}
        piece={'id':'c1','start':0,'duration':2.6,'audio':'pieces/n1-c1.wav','lip_sync':lip_sync}
        info={'streams':[{'codec_type':'video','width':1232,'height':1648,'duration':'2.667'},{'codec_type':'audio','duration':'2.667'}]}
        return SimpleNamespace(task=folder,ffmpeg='ffmpeg',ffprobe='ffprobe',inp={'need':need,'segment':{'id':'p1'},'pieces':[piece]}),info
    def test_talking_avatar_clip_is_blocked_when_it_was_not_driven_by_the_narration(self):
        with tempfile.TemporaryDirectory() as d:
            args,info=self._avatar_args(Path(d),'talking-avatar',.12)
            with patch.object(workflow,'probe',return_value=info),patch.object(workflow,'run'),patch.object(workflow,'audio_diagnostic',return_value=.12):
                with self.assertRaisesRegex(ValueError,'口型'):workflow.command_avatar(args)
    def test_talking_avatar_clip_is_scaled_to_a_bounded_height_and_accepted_when_in_sync(self):
        with tempfile.TemporaryDirectory() as d:
            args,info=self._avatar_args(Path(d),'talking-avatar',.99)
            calls=[]
            with patch.object(workflow,'probe',return_value=info),patch.object(workflow,'run',side_effect=lambda argv,**k:calls.append([str(x) for x in argv])),patch.object(workflow,'audio_diagnostic',return_value=.99),patch.object(workflow,'enrich_avatar_material',return_value=({},[])):
                result=workflow.command_avatar(args)
            normal=next(c for c in calls if c[-1].endswith('n1-c1-normal.mp4'))
            self.assertIn('fps=25,scale=-2:min(ih\\,1280)',normal[normal.index('-vf')+1])
            self.assertFalse(any('音频包络' in w for w in result['warnings']))
    def test_legacy_generated_voice_clip_only_warns(self):
        with tempfile.TemporaryDirectory() as d:
            args,info=self._avatar_args(Path(d),'generated-voice',.12)
            with patch.object(workflow,'probe',return_value=info),patch.object(workflow,'run'),patch.object(workflow,'audio_diagnostic',return_value=.12),patch.object(workflow,'enrich_avatar_material',return_value=({},[])):
                result=workflow.command_avatar(args)
            self.assertTrue(any('口型可能与配音不同步' in w for w in result['warnings']))

    def test_review_preserves_render_when_optional_detector_fails(self):
        with tempfile.TemporaryDirectory() as d:
            folder=Path(d)
            state={'input':{},'shots':[],'segments':[],'materials':[]}
            fake=SimpleNamespace(FaceDetectorYN=SimpleNamespace(create=lambda *a:(_ for _ in ()).throw(RuntimeError('bad model'))))
            with patch.dict('sys.modules',{'cv2':fake}),patch.object(Path,'exists',return_value=True),patch.object(workflow,'read',return_value={}):
                report,_=workflow.review_rendered_shots(SimpleNamespace(task=folder,state=state))
            self.assertEqual(report['face_check'],'unavailable')
            self.assertTrue((folder/'quality-review.json').is_file())

    def test_mg_materials_require_current_content_plan_not_demo_video(self):
        with tempfile.TemporaryDirectory() as d:
            folder=Path(d);workflow.write(folder/'p1/timestamps.json',{'sentences':[]})
            state={'input':{'style':'mg-collage','ratio':'16:9'},'segments':[{'id':'p1','text':'先确认声音。再准备素材。','chapter':'制作流程'}]}
            args=SimpleNamespace(task=folder,state=state,inp={'motion_plan':{'version':'content-motion/1','scenes':[]}})
            need={'segment_id':'p1','duration':3,'content':'声音与素材','template':'steps','items':['确认声音','准备素材']}
            manifest=workflow.template_manifest(args,need)
            self.assertEqual(manifest['items'],['确认声音','准备素材'])
            self.assertEqual(manifest['render_mode'],'content-motion')
            self.assertEqual(manifest['motion_plan'],args.inp['motion_plan'])
            manifest=workflow.template_manifest(args,{**need,'items':['虚构场景']})
            self.assertNotIn('虚构场景',manifest['items'])
            args.inp={}
            with self.assertRaisesRegex(ValueError,'不能使用演示视频'):workflow.template_manifest(args,need)
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
