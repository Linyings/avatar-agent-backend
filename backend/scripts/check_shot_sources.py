"""Read-only source range evidence for a saved F10 response; no model calls."""
import json
import subprocess
import sys
from pathlib import Path

task, plan_file, output = map(Path, sys.argv[1:4])
state = json.loads((task / 'workflow.json').read_text())
plan = json.loads(plan_file.read_text())
segments = {s['id']: s for s in state['segments']}
materials = {m['need_id']: m for m in state['materials']}
probes = {}
for mid, material in materials.items():
    info = json.loads(subprocess.check_output(['ffprobe', '-v', 'error', '-show_streams', '-show_format', '-of', 'json', str(task / material['path'])]))
    video = next(s for s in info['streams'] if s['codec_type'] == 'video')
    probes[mid] = float(video.get('duration') or info['format']['duration'])
rows = []
for shot in plan['shots']:
    segment = segments[shot['segment_id']]
    for role in ['primary', 'overlay']:
        if not shot.get(role):
            continue
        mid = shot[role]; material = materials[mid]
        raw = shot['start'] - segment['start'] if material['kind'] == 'avatar' else shot.get(role + '_in', 0)
        corrected = 0 if material['kind'] == 'avatar' and -1e-9 <= raw < 0 else raw
        used = shot['end'] - shot['start']
        rows.append({'shot': shot['id'], 'role': role, 'material': mid, 'raw_source_in': raw, 'corrected_source_in': corrected, 'used_seconds': used, 'declared_duration': material['duration'], 'probed_video_duration': probes[mid], 'original_valid': raw >= 0 and raw + used <= material['duration'] + .045, 'corrected_valid': corrected >= 0 and corrected + used <= min(material['duration'], probes[mid]) + .045})
report = {'run_id': state['id'], 'model_calls': 0, 'shots': len(plan['shots']), 'source_checks': rows}
output.write_text(json.dumps(report, ensure_ascii=False, indent=2))
print(json.dumps({'original_invalid': [r for r in rows if not r['original_valid']], 'all_corrected_intervals_valid': all(r['corrected_valid'] for r in rows), 'shots': len(plan['shots']), 'model_calls': 0}))
