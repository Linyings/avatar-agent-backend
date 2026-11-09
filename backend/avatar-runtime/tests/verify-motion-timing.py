"""Exercise the Python -> Node timing entry against recorded incident data.

Only a temporary task directory is written. No provider calls or run changes.
"""
import json
from pathlib import Path
import subprocess
import sys
import tempfile

root = Path(__file__).resolve().parents[1]
source = json.loads(Path(sys.argv[1]).read_text())['animation']
with tempfile.TemporaryDirectory(prefix='motion-timing-review-') as temp:
    task = Path(temp)
    (task / 'p1').mkdir()
    need = {'id': 'fixture', 'kind': 'animation', 'segment_id': 'p1',
            'duration': source['duration'], 'content': source['brief'],
            'template': source['template']}
    state = {'input': {'ratio': '16:9', 'style': source['style']}, 'needs': [need],
             'segments': [{'id': 'p1', 'text': source['text'], 'chapter': source['title'],
                           'duration': source['duration']}]}
    files = {'workflow.json': state,
             'media-input.json': {'need': need, 'motion_plan': source['motion_plan']},
             'p1/timestamps.json': {'sentences': [{'words': source['words']}]}}
    for name, data in files.items():
        (task / name).write_text(json.dumps(data, ensure_ascii=False), encoding='utf-8')
    subprocess.run([sys.executable, str(root / 'workflow.py'), 'motion_timing',
                    '--task-dir', str(task)], check=True)
    result = json.loads((task / 'media-result.json').read_text())
    scenes = result['scenes']
    assert len(scenes) == 2, scenes
    assert abs(scenes[1]['duration'] - 3.06525) < .001, scenes[1]
    assert all('local_at' in element and 'enter_at' in element for scene in scenes
               for element in scene['elements'])
    print(json.dumps({'python_node_timing': 'passed', 'scene_seconds': [s['duration'] for s in scenes],
                      'model_calls': 0, 'original_run_modified': False}))
