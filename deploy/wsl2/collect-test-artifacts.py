"""Copy a known local Docker task's media and diagnostics; makes no model calls."""
import argparse
import json
from pathlib import Path
import subprocess
import uuid

parser = argparse.ArgumentParser()
parser.add_argument('task_id', type=uuid.UUID)
args = parser.parse_args()
task_id = str(args.task_id)
source = '/app/data/avatar-mvp/' + task_id
output = Path('/mnt/d/pro/ai_cat/avatar-agent/backend/data/real-test-20261104') / task_id
output.mkdir(parents=True, exist_ok=True)

# Generate diagnostic frames even when the production verifier rejected the clip
# before its QA-frame stage. These files never mark a shot as verified.
diagnose = '''import json, subprocess
from pathlib import Path
task = Path(TASK)
files = ['voice.wav', 'request.json', 'timestamps.json', 'timing.json', 'clips.json',
         'prepare.log', 'verify.log', 'render.log', 'manifest.json', 'output.mp4', 'render-check.json',
         'operations.jsonl', 'SHOTBOOK.md', 'shots.json', 'semantics.json', 'beats.json', 'pipeline.json']
media = []
for index in range(1, 4):
    directory = task / ('shot-' + str(index))
    video = directory / 'source.mp4'
    if video.is_file():
        info = json.loads(subprocess.check_output(['ffprobe', '-v', 'error', '-show_entries',
            'format=duration:stream=codec_type,codec_name,width,height,avg_frame_rate,sample_rate,channels',
            '-of', 'json', str(video)]))
        media.append({'shot': directory.name, **info})
        duration = float(info['format']['duration'])
        for name, sec in [('first', 0), ('middle', duration / 2), ('last', max(0, duration - .1))]:
            subprocess.run(['ffmpeg', '-y', '-v', 'error', '-ss', str(sec), '-i', str(video),
                '-frames:v', '1', str(directory / ('diagnostic-' + name + '.jpg'))], check=True)
    for name in ['source.mp4', 'audio.wav', 'generated.wav', 'audio-check.json', 'verified.json',
                 'speech-check.json', 'face-zone.json', 'qa-first.jpg', 'qa-middle.jpg', 'qa-last.jpg',
                 'diagnostic-first.jpg', 'diagnostic-middle.jpg', 'diagnostic-last.jpg']:
        files.append(directory.name + '/' + name)
print(json.dumps({'files': [name for name in files if (task / name).is_file()], 'media': media}))
'''.replace('TASK', repr(source))
result = subprocess.run(['docker', 'exec', 'aicat-avatar-api-1', 'python3', '-c', diagnose],
                        check=True, capture_output=True, text=True)
inventory = json.loads(result.stdout)
for name in inventory['files']:
    destination = output / name
    destination.parent.mkdir(parents=True, exist_ok=True)
    subprocess.run(['docker', 'cp', 'aicat-avatar-api-1:' + source + '/' + name, str(destination)],
                   check=True, capture_output=True)
(output / 'media-check.json').write_text(json.dumps(inventory['media'], indent=2))
print(json.dumps({'task_id': task_id, 'output': str(output), 'files': inventory['files'],
                  'media': inventory['media']}, ensure_ascii=False, indent=2))
