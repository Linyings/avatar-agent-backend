"""Vendor the limited talkcraft sources with provenance. Run from backend root."""
import argparse
import hashlib
import json
import re
from pathlib import Path
import shutil

backend = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser(description='Explicitly import reviewed talkcraft sources; not needed to build or run.')
parser.add_argument('--source', required=True, type=Path, help='Path to a reference video-talkcraft checkout')
source = parser.parse_args().source.resolve()
runtime = backend / 'avatar-runtime'
entries = {}

def copy(relative, target, transform=lambda s: s):
    original = (source / relative).read_bytes()
    entries[relative] = {'sha256': hashlib.sha256(original).hexdigest(), 'vendored': target}
    destination = runtime / target
    destination.parent.mkdir(parents=True, exist_ok=True)
    destination.write_text(transform(original.decode('utf-8').replace('\r\n', '\n')), encoding='utf-8')

for name in ['timestamps_cpu', 'make_timing', 'face_bbox']:
    transform = (lambda s: s.replace('sys.exit("全程未检出人脸——素材里有人吗？（侧脸/极小脸可把 --step 调小并降阈值）")', 'print("No face detected in generated footage")\n        sys.exit(2)')) if name == 'face_bbox' else (lambda s: s)
    copy(f'scripts/{name}.py', f'talkcraft/{name}.py', transform)
copy('template/motion-systems/camera.tsx', 'src/talkcraft/camera.tsx')

def patch(text, before, after):
    if before not in text:
        raise RuntimeError('Upstream changed; review adapter: ' + before)
    return text.replace(before, after)

def common(text):
    text = text.replace('useCurrentFrame }', 'useCurrentFrame, useVideoConfig }')
    text = text.replace('  const t = useCurrentFrame() / FPS;', '  const t = (useCurrentFrame() - anchorFrame) / useVideoConfig().fps;')
    text = re.sub(r'// 主持人占位：.*?\nconst CSS =', '// Host is composed separately from non-looping generated footage.\nconst CSS =', text, flags=re.S)
    text = text.replace('AbsoluteFill, Loop, OffthreadVideo,', 'AbsoluteFill,')
    return '// Adapted for supplied content, real fps, and external non-looping host composition.\n' + text

def impact(text):
    text = common(text)
    text = patch(text, '({ hostSrc }: { hostSrc?: string })', '({ title, subtitle, anchorFrame = 0 }: { title: string; subtitle: string; anchorFrame?: number })')
    text = patch(text, '<div className="host-wrap"><Host src={hostSrc} /></div>', '')
    text = patch(text, '        三秒抓住', '')
    text = patch(text, '          重点', '          {title}')
    text = patch(text, '接下来这三分钟，只讲清楚一件事', '{subtitle}')
    text = text.replace('font-size: 72px;', 'font-size: 46px;').replace('white-space: nowrap;', 'white-space: normal; max-width: 800px; overflow-wrap: anywhere;')
    text = text.replace('top: 214px;', 'top: 110px;').replace('top: 320px;', 'top: 260px;')
    return text

def highlight(text):
    text = common(text)
    text = patch(text, '(_props: { hostSrc?: string })', '({ title, subtitle, anchorFrame = 0 }: { title: string; subtitle: string; anchorFrame?: number })')
    text = text.replace('《2024 年度宏观经济报告》 · 第 42 页', '关键观点')
    text = text.replace('过去三年，居民部门的储蓄率持续攀升，', '{subtitle}')
    for old in ['消费意愿始终徘徊在低位。报告指出，', '这一判断与多家机构的调研结论一致，', '政策端的回应也在陆续落地。']:
        text = text.replace(old, '')
    text = text.replace('真正拖住消费的不是没钱，而是对未来的不确定感', '{title}')
    text = text.replace('width: 640px;', 'width: 800px;').replace('34px 42px 30px', '20px 32px 20px').replace('font-size: 21px;', 'font-size: 38px;').replace('line-height: 1.9;', 'line-height: 1.4;')
    text = text.replace('.quote-line {', '.quote-line {\n  overflow-wrap: anywhere;')
    return text

def chapter(text):
    text = common(text)
    text = patch(text, '({ hostSrc, themes = DEMO_THEMES }: { hostSrc?: string; themes?: ChapterTheme[] })', '({ title, subtitle, anchorFrame = 0, themes = DEMO_THEMES }: { title: string; subtitle: string; anchorFrame?: number; themes?: ChapterTheme[] })')
    text = patch(text, '      <Host src={hostSrc} />', '')
    text = patch(text, 'name="泡沫是怎么吹起来的" sub="CHAPTER 01 · 2006—2008"', 'name={title} sub={subtitle}')
    text = patch(text, '      <ChapterCard t={t} at={at2} theme={themes[1] ?? themes[0]} num="02"\n        name="谁在最后一刻离场" sub="CHAPTER 02 · 2008.09" />', '')
    # Keep the single summary visible for the narration; no demo second chapter.
    text = patch(text, 'hold: 1.2,', 'hold: 30,')
    text = text.replace('font-size: 216px;', 'font-size: 160px;').replace('.chapter-text { overflow: hidden; }', '.chapter-text { overflow: hidden; width: 600px; }').replace('.chapter-name {', '.chapter-name {\n  max-width: 560px; overflow-wrap: anywhere; line-height: 1.3;')
    return text

for name, transform in [('impact-open-title', impact), ('highlighter-sweep', highlight), ('chapter-title-card', chapter)]:
    copy(f'template/cards/{name}.tsx', f'src/talkcraft/{name}.tsx', transform)

shutil.copy2(source / 'LICENSE', runtime / 'TALKCRAFT-LICENSE')
(runtime / 'UPSTREAM.json').write_text(json.dumps({'repository': 'https://github.com/Vincentwei1021/video-talkcraft', 'notice': 'Copyright (c) 2026 Vincent Wei. PolyForm Noncommercial; commercial use requires authorization.', 'sources': entries}, ensure_ascii=False, indent=2), encoding='utf-8')
print('Vendored limited talkcraft sources:', len(entries))
