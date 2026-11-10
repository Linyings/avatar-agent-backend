import tempfile
import shutil
import subprocess
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch
import workflow


class NativeSpeechTests(unittest.TestCase):
    @unittest.skipUnless(shutil.which('ffmpeg') and shutil.which('ffprobe'), 'requires FFmpeg')
    def test_real_media_concat_preserves_generated_sound_and_duration(self):
        with tempfile.TemporaryDirectory() as d:
            folder=Path(d);(folder/'p1').mkdir()
            pieces=[]
            for i in range(2):
                rel=f'p1/c{i+1}.mp4';pieces.append(rel)
                subprocess.run(['ffmpeg','-y','-v','error','-f','lavfi','-i','color=c=blue:s=160x240:r=25:d=2.4','-f','lavfi','-i',f'sine=frequency={440+i*220}:sample_rate=24000:duration=2.4','-c:v','libx264','-c:a','aac','-shortest',str(folder/rel)],check=True)
            args=SimpleNamespace(task=folder,inp={'segment':{'id':'p1','text':'测试'},'pieces':pieces},ffmpeg='ffmpeg',ffprobe='ffprobe')
            def measured_audio(a):
                return {'duration':workflow.duration(workflow.probe(a,folder/'p1/audio.wav'),'audio'),'warnings':[]}
            with patch.object(workflow,'command_audio',side_effect=measured_audio):
                result=workflow.command_native_speech(args)
            self.assertAlmostEqual(result['duration'],4.8,delta=.12)
            info=workflow.probe(args,folder/result['video'])
            self.assertAlmostEqual(workflow.duration(info,'video'),workflow.duration(info,'audio'),delta=.12)
            # Both tracks decode from the same generated source at the same origin.
            import numpy as np
            def pcm(path):
                raw=subprocess.check_output(['ffmpeg','-v','error','-i',str(path),'-map','0:a:0','-f','f32le','-ar','24000','-ac','1','-'])
                return np.frombuffer(raw,dtype=np.float32)
            video_audio=pcm(folder/result['video']);voice=pcm(folder/result['audio'])
            size=min(len(video_audio),len(voice))
            self.assertGreater(float(np.corrcoef(video_audio[:size],voice[:size])[0,1]),.999)

    def test_audio_reference_requires_real_audio_and_supported_duration(self):
        args=SimpleNamespace(inp={'source':'voice.mp3'},task=Path('.'),ffmpeg='ffmpeg')
        for info in [{'streams':[{'codec_type':'video','duration':'5'}]}, {'streams':[{'codec_type':'audio','duration':'1'}]}]:
            with patch.object(workflow,'probe',return_value=info):
                with self.assertRaises(ValueError):workflow.command_voice_reference(args)

    def test_native_video_with_no_sound_is_rejected(self):
        with tempfile.TemporaryDirectory() as d:
            args=SimpleNamespace(task=Path(d),inp={'segment':{'id':'p1','text':'原稿'},'pieces':['p1/c1.mp4']},ffmpeg='ffmpeg')
            with patch.object(workflow,'probe',return_value={'streams':[{'codec_type':'video','duration':'5'}]}):
                with self.assertRaisesRegex(ValueError,'声音'):workflow.command_native_speech(args)

    def test_normalization_keeps_generated_audio_and_never_retimes(self):
        with tempfile.TemporaryDirectory() as d:
            folder=Path(d);calls=[]
            args=SimpleNamespace(task=folder,inp={'segment':{'id':'p1','text':'原稿'},'pieces':['p1/c1.mp4']},ffmpeg='ffmpeg')
            info={'streams':[{'codec_type':'video','duration':'5.24','width':720,'height':1280},{'codec_type':'audio','duration':'5.24'}]}
            with patch.object(workflow,'probe',return_value=info),patch.object(workflow,'run',side_effect=lambda argv,**kw:calls.append([str(x) for x in argv])),patch.object(workflow,'command_audio',return_value={'duration':5.24,'warnings':[]}):
                result=workflow.command_native_speech(args)
            self.assertEqual(result['duration'],5.24)
            normalize=next(c for c in calls if c[-1].endswith('-normal.mkv'))
            self.assertIn('0:a:0',normalize)
            self.assertFalse(any('atempo' in x or 'setpts=' in x or x=='-an' for c in calls for x in c))

if __name__=='__main__':unittest.main()
