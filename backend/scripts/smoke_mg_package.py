"""Verify a downloadable package renders without the installed source tree."""
import json
import subprocess
import sys
import zipfile
from pathlib import Path

root = Path(sys.argv[1]); root.mkdir(parents=True, exist_ok=True)
runtime = Path('/app/avatar-runtime')
with zipfile.ZipFile(runtime / 'catalog/mg-flat-vector-source.zip') as archive:
    archive.extractall(root)
(root / 'node_modules').symlink_to(runtime / 'node_modules', target_is_directory=True)
animation = {'style': 'mg-flat-vector', 'width': 480, 'height': 270, 'fps': 25, 'duration': 1.2, 'title': '源码包验证', 'text': '从源码制作动画。', 'brief': '独立解包后渲染', 'words': []}
(root / 'verify.mjs').write_text("import {renderNative} from './mg-native.mjs';\nawait renderNative(" + json.dumps(animation, ensure_ascii=False) + ",new URL('./verified.mp4',import.meta.url).pathname);\n")
subprocess.run(['node', str(root / 'verify.mjs')], cwd=runtime, check=True)
info = json.loads(subprocess.check_output(['ffprobe', '-v', 'error', '-show_streams', '-of', 'json', str(root / 'verified.mp4')]))
video = next(s for s in info['streams'] if s['codec_type'] == 'video')
assert (video['width'], video['height'], int(video['nb_frames'])) == (480, 270, 30)
report = {'package': 'mg-flat-vector-source.zip', 'independent_extracted_source': True, 'shared_dependencies': 'identical node_modules and installed browser/fonts', 'frames': 30, 'model_calls': 0, 'status': 'passed'}
(root / 'package-report.json').write_text(json.dumps(report, ensure_ascii=False, indent=2))
print(json.dumps(report))
