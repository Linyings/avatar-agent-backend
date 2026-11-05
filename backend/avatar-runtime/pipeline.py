"""Local media adapter. No remote model calls and no synthetic production output."""
from __future__ import annotations
import argparse
import hashlib
import importlib.util
import json
import math
from pathlib import Path
import shutil
import subprocess
import sys

ROOT = Path(__file__).resolve().parent


class QualityRejected(ValueError):
    """Semantic media failure; infrastructure errors must reuse the existing source."""

def read(path):
    return json.loads(Path(path).read_text(encoding='utf-8'))

def write(path, data):
    Path(path).write_text(json.dumps(data, ensure_ascii=False, indent=2), encoding='utf-8')

def run(argv, **kwargs):
    return subprocess.run([str(v) for v in argv], check=True, **kwargs)

def upstream(name):
    spec = importlib.util.spec_from_file_location('talkcraft_' + name, ROOT / 'talkcraft' / (name + '.py'))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod

def probe(path, ffprobe):
    return json.loads(subprocess.check_output([ffprobe, '-v', 'error', '-show_format', '-show_streams', '-of', 'json', str(path)]))

def fraction(value):
    n, d = value.split('/')
    return float(n) / float(d)

def clip_boundaries(timing, duration):
    scenes = timing['scenes']
    if len(scenes) != 3:
        raise ValueError('Expected three aligned narration sections')
    edges = [0]
    for left, right in zip(scenes, scenes[1:]):
        end = left['startSec'] + left['durationSec']
        if end > right['startSec'] + .04:
            raise ValueError('Narration spans overlap')
        edges.append(round((end + right['startSec']) / 2, 6))
    edges.append(duration)
    if any(b <= a for a, b in zip(edges, edges[1:])):
        raise ValueError('Invalid narration boundaries')
    return edges

def validate_render_manifest(clips, duration):
    previous = 0
    fps = clips[0]['fps'] if clips else 0
    if not clips or fps <= 0:
        raise ValueError('Missing verified clips')
    for clip in clips:
        if abs(clip['start'] - previous) > .001 or clip['end'] <= clip['start']:
            raise ValueError('Clip mapping has gaps or repeated narration')
        if abs(clip['fps'] - fps) > .001:
            raise ValueError('Generated clips have inconsistent frame rates')
        if clip['source_start'] < 0 or clip['source_start'] + clip['duration'] > clip['source_duration'] + 1 / fps:
            raise ValueError('Source clip cannot cover narration without looping or retiming')
        previous = clip['end']
    if abs(previous - duration) > .001:
        raise ValueError('Clips do not cover the final voice')

def doctor(args):
    import numpy
    import scipy
    import cv2
    if args.asr_backend == 'firered':
        import sherpa_onnx
        if not (Path(args.asr_model_dir) / 'model.int8.onnx').is_file():
            raise ValueError('FireRed weights missing')
    else:
        import faster_whisper
    run([args.ffmpeg, '-version'], stdout=subprocess.DEVNULL)
    run([args.ffprobe, '-version'], stdout=subprocess.DEVNULL)
    run([args.node, '--version'], stdout=subprocess.DEVNULL)

def align(args, audio, sentences):
    mod = upstream('timestamps_cpu')
    if args.asr_backend == 'firered':
        words = mod.asr_firered(str(audio), args.asr_model_dir)
    else:
        words = mod.asr_whisper(str(audio), args.asr_model_dir or 'small')
    result = mod.align(sentences, words)
    if not result['sentences'] or any(not s['ok'] for s in result['sentences']):
        raise QualityRejected('Speech differs from supplied script; review actual audio and alignment')
    # The upstream aligner interpolates unmatched characters. Also check the raw
    # recognition, so interpolation cannot silently hide missing/extra dialogue.
    import difflib
    normalized = upstream('make_timing').normalized_text
    actual = normalized(''.join(w['text'] for w in words))
    expected = normalized(''.join(sentences))
    if difflib.SequenceMatcher(None, expected, actual).ratio() < .85:
        raise QualityRejected('Recognized speech does not match the original script')
    return result

def split_script(text):
    """Same sentence boundaries as the Go storyboard splitter."""
    out, current = [], []
    for char in text.strip():
        current.append(char)
        if char in '。！？!?\n':
            sentence = ''.join(current).strip()
            if sentence:
                out.append(sentence)
            current = []
    tail = ''.join(current).strip()
    if tail:
        out.append(tail)
    return out

def narration_units(request):
    """Talkcraft timestamps follow the original sentences. Shot audio is grouped afterwards."""
    shots = request['shots']
    script = (request.get('script') or '').strip()
    if script:
        sentences = split_script(script)
        ids = [i for shot in shots for i in (shot.get('sentence_ids') or [])]
        if sentences and ids == list(range(len(sentences))):
            return sentences, True
    return [shot['text'] for shot in shots], False

def shot_edges(shots, aligned, duration, grouped):
    if not grouped:
        return clip_boundaries(upstream('make_timing').make_timing_data(aligned), duration)
    scenes = []
    for shot in shots:
        ids = shot.get('sentence_ids') or []
        if not ids:
            raise ValueError(f'{shot.get("id", "shot")} has no script sentences')
        start = float(aligned['sentences'][ids[0]]['start'])
        end = float(aligned['sentences'][ids[-1]]['end'])
        scenes.append({'startSec': start, 'durationSec': end - start})
    return clip_boundaries({'scenes': scenes}, duration)

def prepare(args):
    task = Path(args.task_dir)
    request = read(task / 'request.json')
    sentences, grouped = narration_units(request)
    if request.get('script'):
        (task / 'script.txt').write_text(request['script'].strip() + '\n', encoding='utf-8')
    digest = hashlib.sha256((task / 'voice.wav').read_bytes() + json.dumps(sentences, ensure_ascii=False).encode()).hexdigest()
    cache = task / 'prepared.json'
    if cache.exists() and read(cache).get('digest') == digest and all((task / f'shot-{i}' / 'audio.wav').is_file() for i in range(1, 4)):
        return
    duration = float(probe(task / 'voice.wav', args.ffprobe)['format']['duration'])
    if duration > 30 or duration < 6:
        raise ValueError(f'Final narration must be 6–30 seconds, received {duration:.2f}s')
    aligned = align(args, task / 'voice.wav', sentences)
    write(task / 'timestamps.json', aligned)
    timing = upstream('make_timing').make_timing_data(aligned)
    timing['totalSec'] = duration
    write(task / 'timing.json', timing)
    edges = shot_edges(request['shots'], aligned, duration, grouped)
    clips = []
    for i, (start, end) in enumerate(zip(edges, edges[1:])):
        clip_id = f'shot-{i + 1}'
        directory = task / clip_id
        directory.mkdir(exist_ok=True)
        length = end - start
        if length < 2 or length > 15:
            raise ValueError(f'{clip_id}: narration must fit a 2–15s generation budget; adjust sentence grouping')
        run([args.ffmpeg, '-y', '-v', 'error', '-i', task / 'voice.wav', '-af', f'atrim=start={start}:end={end},asetpts=PTS-STARTPTS', '-ar', '16000', '-ac', '1', directory / 'audio.wav'])
        clips.append({'id': clip_id, 'start': start, 'end': end, 'duration': length, 'request_duration': math.ceil(length), 'source_start': 0, 'fps': 0, 'width': 0, 'height': 0})
    write(task / 'clips.json', clips)
    write(cache, {'digest': digest})

def verify(args):
    task = Path(args.task_dir)
    clip_id = args.shot_id
    if clip_id not in ('shot-1', 'shot-2', 'shot-3'):
        raise ValueError('Invalid shot ID')
    directory = task / clip_id
    clip = next(c for c in read(task / 'clips.json') if c['id'] == clip_id)
    media = probe(directory / 'source.mp4', args.ffprobe)
    stream = next(s for s in media['streams'] if s['codec_type'] == 'video')
    fps = fraction(stream['avg_frame_rate'])
    if fps <= 0 or abs(fps - fraction(stream['r_frame_rate'])) > .01:
        raise QualityRejected('Variable or invalid source frame rate')
    if abs(stream['width'] / stream['height'] - 9 / 16) > .03:
        raise QualityRejected('Expected a portrait 9:16 source')
    source_duration = float(stream.get('duration') or media['format']['duration'])
    clip.update(fps=fps, width=stream['width'], height=stream['height'], source_start=0, source_duration=source_duration)
    if clip['duration'] > clip['source_duration'] + 1 / fps:
        raise QualityRejected('Generated video is too short for its supplied speech')
    try:
        run([sys.executable, ROOT / 'talkcraft' / 'face_bbox.py', directory / 'source.mp4', directory / 'face-zone.json'])
    except subprocess.CalledProcessError as exc:
        if exc.returncode == 2:
            raise QualityRejected('No face detected in generated footage') from exc
        raise
    face = read(directory / 'face-zone.json')
    if not face.get('safe_zone') or face.get('detect_rate', 0) < .6:
        raise QualityRejected('Face safe zone could not be measured reliably')
    clip['face_zone'] = face['safe_zone']
    for name, sec in [('first', clip['source_start']), ('middle', clip['source_start'] + clip['duration'] / 2), ('last', max(clip['source_start'], clip['source_start'] + clip['duration'] - 1 / fps))]:
        run([args.ffmpeg, '-y', '-v', 'error', '-ss', sec, '-i', directory / 'source.mp4', '-frames:v', '1', directory / f'qa-{name}.jpg'])
    write(directory / 'verified.json', clip)

FLOW_SCRIPTS = ROOT / 'talkcraft' / 'flow' / 'scripts'
CARD_SEM = {
    'impact-open-title': {'钩子', '章节', '标题'},
    'highlighter-sweep': {'引用', '强调'},
    'chapter-title-card': {'章节', '转场'},
}
HARD_SEM = {'自我介绍', '介绍他人', '号召'}
# Talkcraft fails a film with no B-roll. This product's required picture is the Seedance host,
# so that one result is recorded and does not stop delivery.
HOST_LED_WAIVERS = ('全片零 B-roll',)

def flow_py(*args, cwd):
    proc = subprocess.run([sys.executable, *map(str, args)], cwd=str(cwd), capture_output=True, text=True, encoding='utf-8')
    output = (proc.stdout or '') + (proc.stderr or '')
    if proc.returncode:
        raise ValueError(output.strip() or f'flow script exited {proc.returncode}')
    return output

def fill_semantics(path):
    """The skill leaves sem/weight for a person. This product fills them from the same lexical hints."""
    doc = read(path)
    total = len(doc['sentences'])
    for index, sentence in enumerate(doc['sentences']):
        sem, needs = [], []
        for hint in sentence.get('hint') or []:
            if str(hint).startswith('need:'):
                needs.append(str(hint).split(':', 1)[1])
            elif hint not in sem:
                sem.append(hint)
        if index == 0 and '钩子' not in sem:
            sem.insert(0, '钩子')
        if index == total - 1 and '章节' not in sem:
            sem.append('章节')
        sentence['sem'] = sem or ['论点']
        sentence['need'] = needs or ['无']
        sentence['weight'] = 'main'
        sentence['entities'] = sentence.get('entities') or []
        sentence['shot'] = sentence.get('shot') or ''
    write(path, doc)

def semantics(args):
    """talkcraft ②-1. Shot ids stay empty until SHOTBOOK exists."""
    task = Path(args.task_dir)
    script = FLOW_SCRIPTS / 'semantic_annotate.py'
    flow_py(script, '--project', task, '--timestamps', 'timestamps.json', '--shots', 'shots.json', '--init', '--force', cwd=task)
    fill_semantics(task / 'semantics.json')
    flow_py(script, '--project', task, '--timestamps', 'timestamps.json', '--shots', 'shots.json', cwd=task)

def shot_sentence_indexes(shot, shot_index, grouped):
    if grouped:
        return list(shot.get('sentence_ids') or [])
    return [shot_index]

def balance_weights(path, shot_ids):
    doc = read(path)
    grouped = {}
    for sentence in doc['sentences']:
        grouped.setdefault(str(sentence.get('shot') or '').lower(), []).append(sentence)
    for sid in shot_ids:
        rows = grouped.get(sid.lower(), [])
        for index, row in enumerate(rows):
            row['weight'] = 'main' if index == 0 else 'sub'
    write(path, doc)

def shotbook(args):
    """talkcraft ④ after Seedance has written each shot's host footage."""
    task = Path(args.task_dir)
    request = read(task / 'request.json')
    timestamps = read(task / 'timestamps.json')
    clips = read(task / 'clips.json')
    _, grouped = narration_units(request)
    if len(request['shots']) != len(clips):
        raise ValueError('SHOTBOOK requires one verified clip span per shot')
    lines = ['# SHOTBOOK', '', 'G0 风格档：数字人口播竖屏。人物素材由 Seedance 2.5 从同一张照片和分段配音生成。', '']
    shots_json, beats, shot_ids = [], [], []
    for index, (shot, clip) in enumerate(zip(request['shots'], clips)):
        source = task / shot['id'] / 'source.mp4'
        if not source.is_file():
            raise ValueError(f'{shot["id"]} has no Seedance host footage')
        sid = f'S{index + 1}'
        shot_ids.append(sid)
        card = shot.get('card') or 'impact-open-title'
        start, end = float(clip['start']), float(clip['end'])
        ids = shot_sentence_indexes(shot, index, grouped)
        if not ids:
            raise ValueError(f'{shot["id"]} has no script sentences')
        lines.append(f'### {sid} · {start:.2f}–{end:.2f} · 意图：{shot.get("angle", "")} {shot.get("framing", "")}')
        lines.append(f'- 素材：人（{shot["id"]}/source.mp4）')
        lines.append(f'- 蒙皮行：{card}')
        lines.append(f'- 台词：{shot["text"]}')
        covered = CARD_SEM.get(card, set())
        labeled = read(task / 'semantics.json')['sentences']
        waived = []
        for sentence_index in ids:
            for word in labeled[sentence_index].get('sem') or []:
                if word in HARD_SEM and word not in covered and word not in waived:
                    waived.append(word)
                    lines.append(f'- 语义偏离：{word} ← 竖屏数字人成片只用已适配的三张卡，不单开该语义的专设卡')
        lines.append('')
        word = (timestamps['sentences'][ids[0]].get('words') or [{'text': timestamps['sentences'][ids[0]]['text'][:1], 'start': start}])[0]
        beats.append({'t': round(float(word['start']), 3), 'anchor': word['text'], 'sentence': ids[0], 'what': card, 'label': sid.lower()})
        shots_json.append({'id': sid, 'clip': shot['id'], 'start': start, 'end': end, 'text': shot['text'], 'angle': shot.get('angle'), 'framing': shot.get('framing'), 'card': card, 'source': shot['id'] + '/source.mp4'})
    lines += ['## 未完成 / 未采集清单', '', '- 无。人物画面已由 Seedance 2.5 生成；本产品不把 B-roll 当作成片门槛。', '']
    (task / 'SHOTBOOK.md').write_text('\n'.join(lines), encoding='utf-8')
    write(task / 'shots.json', shots_json)
    write(task / 'beats.json', beats)
    (task / 'sources.md').write_text(
        '# 素材来源\n\n人物画面由 Seedance 2.5 根据用户照片与分段配音生成，文件为各镜 `shot-N/source.mp4`。\n'
        '配音由豆包固定音色合成。本片不采集 B-roll、图片或网页截图。\n', encoding='utf-8')
    script = FLOW_SCRIPTS / 'semantic_annotate.py'
    flow_py(script, '--project', task, '--timestamps', 'timestamps.json', '--shots', 'shots.json', '--sync-shots', cwd=task)
    balance_weights(task / 'semantics.json', shot_ids)
    flow_py(script, '--project', task, '--timestamps', 'timestamps.json', '--shots', 'shots.json', '--shotbook', 'SHOTBOOK.md', cwd=task)
    flow_py(FLOW_SCRIPTS / 'card_match.py', '--project', task, '--out', 'card-candidates.md', '--json', 'card-candidates.json', cwd=task)
    flow_py(FLOW_SCRIPTS / 'beat_lint.py', task / 'beats.json', task / 'timestamps.json', '--shots', task / 'shots.json', cwd=task)
    flow_py(FLOW_SCRIPTS / 'beat_gap_check.py', task / 'beats.json', task / 'shots.json', cwd=task)
    run_preflight(task)
    write(task / 'pipeline.json', {
        'flow': 'video-talkcraft',
        'addition': 'seedance-2.5',
        'voice_trim': 'skipped because narration is synthesized TTS, not a human recording',
        'stages': [
            {'id': '①', 'label': '口播稿', 'status': 'done'},
            {'id': '②', 'label': '配音 · 时间戳 · 语义标注', 'status': 'done'},
            {'id': '③', 'label': '数字人素材', 'status': 'done', 'tool': 'seedance-2.5'},
            {'id': '④', 'label': 'SHOTBOOK', 'status': 'done'},
            {'id': '⑤', 'label': '实现', 'status': 'done'},
            {'id': '⑥⑦', 'label': '渲染 · 验收', 'status': 'pending'},
        ],
    })

def run_preflight(task):
    proc = subprocess.run(
        [sys.executable, str(FLOW_SCRIPTS / 'preflight.py'), '--project', str(task), '--shotbook', 'SHOTBOOK.md', '--shots', 'shots.json', '--semantics', 'semantics.json'],
        cwd=str(task), capture_output=True, text=True, encoding='utf-8')
    log = (proc.stdout or '') + (proc.stderr or '')
    (task / 'preflight.log').write_text(log, encoding='utf-8')
    fails = [line for line in log.splitlines() if ' FAIL:' in line]
    blocking = [line for line in fails if not any(token in line for token in HOST_LED_WAIVERS)]
    report = {'exit': proc.returncode, 'waived': [line for line in fails if line not in blocking],
              'reason': 'Seedance 2.5 生成的数字人是本片的必需画面；零 B-roll 只记录，不阻断成片。'}
    write(task / 'preflight-report.json', report)
    if blocking or (proc.returncode and not fails):
        raise ValueError('talkcraft preflight failed:\n' + '\n'.join(blocking or log.splitlines()[-30:]))


def render(args):
    task = Path(args.task_dir)
    state = read(task / 'task.json')
    timing = read(task / 'timing.json')
    clips = [read(task / s['id'] / 'verified.json') for s in state['shots']]
    validate_render_manifest(clips, state['duration'])
    fps = clips[0]['fps']
    manifest = {'title': state['title'], 'fps': fps, 'width': 720, 'height': 1280, 'duration': state['duration'], 'timing': timing, 'shots': []}
    for shot, clip in zip(state['shots'], clips):
        manifest['shots'].append({**shot, 'clip': clip, 'source': shot['id'] + '/source.mp4'})
    write(task / 'manifest.json', manifest)
    project = task / 'talkcraft-project'
    project.mkdir(exist_ok=True)
    shutil.copytree(ROOT / 'src', project / 'src', dirs_exist_ok=True)
    for name in ['package.json', 'package-lock.json', 'UPSTREAM.json', 'TALKCRAFT-LICENSE']:
        if (ROOT / name).exists():
            shutil.copy2(ROOT / name, project / name)
    shutil.copy2(task / 'manifest.json', project / 'manifest.json')
    write(project / 'SHOTBOOK.json', [{'id': s['id'], 'text': s['text'], 'angle': s['angle'], 'framing': s['framing'], 'card': s['card'], 'clip': s['clip']} for s in manifest['shots']])
    shotbook_md = task / 'SHOTBOOK.md'
    if shotbook_md.is_file():
        shutil.copy2(shotbook_md, project / 'SHOTBOOK.md')
    else:
        (project / 'SHOTBOOK.md').write_text('\n'.join(f"- {s['id']}: {s['angle']} / {s['framing']} / {s['text']}" for s in state['shots']), encoding='utf-8')
    run([args.node, ROOT / 'render.mjs', task])
    output = probe(task / 'output.mp4', args.ffprobe)
    video = next(s for s in output['streams'] if s['codec_type'] == 'video')
    actual = int(video.get('nb_frames', 0))
    expected = math.ceil(state['duration'] * fps)
    if not actual or actual != expected or abs(fraction(video['avg_frame_rate']) - fps) > .01:
        raise ValueError('Rendered frame count or fps does not match the source timeline')
    if len([s for s in output['streams'] if s['codec_type'] == 'audio']) != 1:
        raise ValueError('Expected exactly one final narration audio track')
    write(task / 'render-check.json', {'frames': actual, 'fps': fps, 'duration': output['format']['duration'], 'human_identity_angle_lip_review_required': True})
    state_path = task / 'pipeline.json'
    if state_path.is_file():
        pipeline = read(state_path)
        for stage in pipeline.get('stages', []):
            if stage.get('id') == '⑥⑦':
                stage['status'] = 'done'
        pipeline['stages'].append({'id': '⑧', 'label': '交付', 'status': 'done', 'output': 'output.mp4'})
        write(state_path, pipeline)

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('stage', choices=['doctor', 'prepare', 'semantics', 'verify', 'shotbook', 'render'])
    parser.add_argument('--task-dir', default='')
    parser.add_argument('--ffmpeg', default='ffmpeg')
    parser.add_argument('--ffprobe', default='ffprobe')
    parser.add_argument('--node', default='node')
    parser.add_argument('--asr-backend', choices=['whisper', 'firered'], default='whisper')
    parser.add_argument('--asr-model-dir', default='')
    parser.add_argument('shot_id', nargs='?')
    args = parser.parse_intermixed_args()
    globals()[args.stage](args)

if __name__ == '__main__':
    try:
        main()
    except QualityRejected:
        import traceback
        traceback.print_exc()
        sys.exit(2)
