import unittest
from pathlib import Path
from pipeline import clip_boundaries, validate_render_manifest, split_script, semantics, shotbook, read, write

class PipelineTests(unittest.TestCase):
    def test_sentence_boundaries_include_initial_and_final_silence(self):
        timing = {'scenes': [{'startSec': .2, 'durationSec': 2}, {'startSec': 2.8, 'durationSec': 2}, {'startSec': 5.2, 'durationSec': 2}]}
        self.assertEqual(clip_boundaries(timing, 8), [0, 2.5, 5, 8])

    def test_script_splitter_matches_storyboard_boundaries(self):
        self.assertEqual(split_script('开场。讲解。总结。'), ['开场。', '讲解。', '总结。'])
        self.assertEqual(split_script('没有句号'), ['没有句号'])

    def test_talkcraft_flow_writes_shotbook_and_waives_only_missing_broll(self):
        import tempfile
        from argparse import Namespace
        task = Path(tempfile.mkdtemp())
        sentences = []
        for index, text in enumerate(('开场讲清楚。', '中间把理由说完。', '最后收住。')):
            words = [{'text': ch, 'start': index * 5 + n * .2, 'end': index * 5 + n * .2 + .1} for n, ch in enumerate(text) if ch not in '。']
            sentences.append({'i': index, 'text': text, 'start': index * 5 + .2, 'end': index * 5 + 2, 'match': 1, 'ok': True, 'words': words})
        write(task / 'timestamps.json', {'sr': 16000, 'total': 15, 'sentences': sentences})
        write(task / 'request.json', {'title': '测试', 'script': ''.join(s['text'] for s in sentences), 'shots': [
            {'id': f'shot-{i + 1}', 'text': s['text'], 'sentence_ids': [i], 'angle': '正面', 'framing': '中景', 'card': card}
            for i, (s, card) in enumerate(zip(sentences, ('impact-open-title', 'highlighter-sweep', 'chapter-title-card')))
        ]})
        write(task / 'clips.json', [{'id': f'shot-{i + 1}', 'start': i * 5, 'end': (i + 1) * 5, 'duration': 5} for i in range(3)])
        for i in range(1, 4):
            directory = task / f'shot-{i}'
            directory.mkdir()
            (directory / 'source.mp4').write_bytes(b'fixture')
        args = Namespace(task_dir=str(task))
        semantics(args)
        shotbook(args)
        book = (task / 'SHOTBOOK.md').read_text(encoding='utf-8')
        self.assertIn('### S1 ·', book)
        self.assertIn('Seedance 2.5', book)
        report = read(task / 'preflight-report.json')
        self.assertTrue(report['waived'])
        self.assertTrue((task / 'card-candidates.md').is_file())
        self.assertTrue((task / 'semantics.json').is_file())

    def test_manifest_rejects_short_video_or_replayed_audio(self):
        clips = [{'start': 0, 'end': 5, 'duration': 5, 'fps': 25, 'source_start': 0, 'source_duration': 4}]
        with self.assertRaises(ValueError):
            validate_render_manifest(clips, 5)

if __name__ == '__main__':
    unittest.main()
