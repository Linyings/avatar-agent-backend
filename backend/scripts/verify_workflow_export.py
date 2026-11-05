"""Re-render an exported project in isolation. Never calls model APIs."""
import argparse
import json
from pathlib import Path
import subprocess
import zipfile

p = argparse.ArgumentParser()
p.add_argument('archive')
p.add_argument('destination')
args = p.parse_args()
destination = Path(args.destination).resolve()
destination.mkdir(parents=True, exist_ok=False)
with zipfile.ZipFile(args.archive) as archive:
    for entry in archive.infolist():
        if not (destination / entry.filename).resolve().is_relative_to(destination):
            raise ValueError('Unsafe archive entry')
    archive.extractall(destination)
# Reuse only the installed, lockfile-identical dependency tree; all project
# sources, media and timeline must come from the actual exported archive.
runtime = Path('/app/avatar-runtime')
assert (destination/'package-lock.json').read_bytes() == (runtime/'package-lock.json').read_bytes()
(destination/'node_modules').symlink_to(runtime/'node_modules', target_is_directory=True)
with (destination/'export-render.log').open('w') as output:
    subprocess.run(['node', str(destination/'workflow-render.mjs'), 'render', str(destination)], cwd=destination, stdout=output, stderr=subprocess.STDOUT, timeout=900, check=True)
info = json.loads(subprocess.check_output(['ffprobe','-v','error','-show_streams','-show_format','-of','json',str(destination/'output.mp4')]))
props = json.loads((destination/'workflow-props.json').read_text())['manifest']
video = next(s for s in info['streams'] if s['codec_type']=='video')
assert any(s['codec_type']=='audio' for s in info['streams'])
assert video['width']==props['width'] and video['height']==props['height']
assert abs(float(info['format']['duration'])-props['duration']) < .12
report = {'archive':str(Path(args.archive).name),'render':'passed','video':{k:video[k] for k in ['width','height','nb_frames']},'duration':info['format']['duration'],'has_audio':True,'model_calls':0}
(destination/'export-report.json').write_text(json.dumps(report, indent=2))
print(json.dumps(report))
