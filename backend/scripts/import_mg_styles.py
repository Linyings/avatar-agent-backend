"""Import pinned, licensed animation sources. Never modify the reference checkout."""
from pathlib import Path
import hashlib,json,shutil,subprocess,sys

source=Path(sys.argv[1]).resolve()
runtime=Path(__file__).resolve().parents[1]/'avatar-runtime'
commit=subprocess.check_output(['git','-C',str(source),'rev-parse','HEAD'],text=True).strip()
site=(source/'site/data.js').read_text(encoding='utf-8')
specs=json.loads(site[site.index('['):].strip().rstrip(';'))
up=runtime/'mg-upstream';up.mkdir(exist_ok=True)
catalog=runtime/'catalog';catalog.mkdir(exist_ok=True)
shutil.copy2(source/'LICENSE',up/'LICENSE')
shutil.copytree(source/'assets',up/'assets',dirs_exist_ok=True)
# The source repository excludes its font binaries. Use fonts installed in our
# image, explicitly recorded below; no hidden network requests at render time.
fonts=up/'assets/fonts';fonts.mkdir(parents=True,exist_ok=True)
import re
families=sorted(set(re.findall(r"font-family:\s*['\"]([^'\"]+)",(source/'assets/fonts/fonts.css').read_text(encoding='utf-8'))))
(fonts/'fonts.css').write_text('\n'.join('@font-face{font-family:'+json.dumps(f)+';src:local("'+('Noto Serif CJK SC' if 'Serif' in f or 'Garamond' in f else 'Noto Sans CJK SC')+'");font-weight:100 1000;}' for f in families),encoding='utf-8')
rows=[]
for spec in specs:
    folder=spec['slug'];sid='mg-'+folder.split('-',1)[1];name=' · '.join(spec['name']);signature=spec['how'];bg=spec['accent']
    # Preserve original effects as fixed video assets when Blender plates are
    # absent or the original raymarcher is impractical on the WSL software GPU.
    production='original-video' if folder in ['04-3d-render','07-liquid'] else 'native-source'
    dest=up/'demos'/folder
    shutil.copytree(source/'demos'/folder,dest,dirs_exist_ok=True,ignore=shutil.ignore_patterns('out','work','__pycache__'))
    template=runtime/'templates'/sid;template.mkdir(parents=True,exist_ok=True)
    prompt=source/'prompts'/(folder+'.md')
    shutil.copy2(prompt,template/'original-prompt.md');shutil.copy2(source/'LICENSE',template/'LICENSE')
    shutil.copy2(source/'videos'/(folder+'.mp4'),catalog/(sid+'-reference.mp4'))
    shutil.copy2(source/'videos'/(folder+'.jpg'),catalog/(sid+'-reference.jpg'))
    shutil.copy2(source/'videos'/(folder+'.mp4'),catalog/(sid+'.mp4'))
    shutil.copy2(source/'videos'/(folder+'.jpg'),catalog/(sid+'.jpg'))
    files={str(p.relative_to(up)).replace('\\','/'):hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(dest.rglob('*')) if p.is_file() and (source/p.relative_to(up)).is_file()}
    info={'repository':'https://github.com/Vincentwei1021/mg-styles-15','commit':commit,'author':'Vincent Wei','license':'MIT','version':'mg-native/3','source':folder,'production_mode':production,'implementation':signature,'prompt_source':'prompts/'+folder+'.md','prompt_sha256':hashlib.sha256(prompt.read_bytes()).hexdigest(),'files':files,'reference_sha256':hashlib.sha256((source/spec['video']).read_bytes()).hexdigest(),'poster_sha256':hashlib.sha256((source/spec['poster']).read_bytes()).hexdigest(),'font_adaptation':'Font binaries excluded upstream; bundled open fonts where needed and installed Noto fallback. See mg-upstream/FONT-SOURCES.json. Original video previews are unchanged.','adaptation':'Original geometry and motion retained; reviewed text substitutions only, timeline mapped to narration duration. No generated replacement graphics. 3D uses the original final video because Blender plates are absent; Liquid uses its original final video because its raymarcher is expensive on the WSL software GPU. Baked text in those two films is not editable.'}
    (template/'SOURCE.json').write_text(json.dumps(info,ensure_ascii=False,indent=2),encoding='utf-8')
    description='直接复用原始动画源码、图形和运镜；适配配音时长，保留原案例视觉结构。'
    if production=='original-video':description='直接复用原版3D成片素材（仓库未包含Blender序列帧）；适配时长，原片内嵌文字不可修改。'
    if folder=='07-liquid':description='直接复用原版液态成片素材（保留源码；当前软件渲染开销高）；适配时长，原片内嵌文字不可修改。'
    rows.append({'id':sid,'name':name,'version':'mg-native/3','source':folder,'entry':'demos/'+folder+'/index.html','signature':signature,'background':bg,'description':description,'ratios':['9:16','16:9','1:1'],'templates':['steps','compare','relationship','highlight','summary','data'],'preview_kind':'upstream-original','preview_width':spec['w'],'preview_height':spec['h'],'production_mode':production})
(runtime/'native-styles.json').write_text(json.dumps(rows,ensure_ascii=False,indent=2),encoding='utf-8')
print(json.dumps({'imported':len(rows),'commit':commit,'source_files':sum(len(json.loads((runtime/'templates'/r['id']/'SOURCE.json').read_text(encoding='utf-8'))['files']) for r in rows)},ensure_ascii=False))
