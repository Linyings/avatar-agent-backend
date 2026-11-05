"""Build downloadable original template packages, with source and license records."""
from pathlib import Path
import json,zipfile
root=Path(__file__).resolve().parent
styles=json.loads((root/'native-styles.json').read_text(encoding='utf-8'))
for style in styles:
    sid=style['id']
    with zipfile.ZipFile(root/'catalog'/(sid+'-source.zip'),'w',zipfile.ZIP_DEFLATED) as z:
        for name in ['mg-native.mjs','mg-shell.js','package.json','package-lock.json']:
            z.write(root/name,name)
        z.writestr('native-styles.json',json.dumps([style],ensure_ascii=False,indent=2))
        for folder in ['mg-upstream/demos/'+style['source'],'mg-upstream/assets','templates/'+sid]:
            for p in (root/folder).rglob('*'):
                if p.is_file() and '__pycache__' not in str(p):z.write(p,p.relative_to(root).as_posix())
        for name in ['LICENSE','FONT-SOURCES.json']:
            z.write(root/'mg-upstream'/name,'mg-upstream/'+name)
        for kind in ['reference.mp4','reference.jpg']:
            p=root/'catalog'/(sid+'-'+kind);z.write(p,'catalog/'+p.name)
        z.writestr('render-example.mjs',"import {renderNative} from './mg-native.mjs';\nawait renderNative("+json.dumps({'style':sid,'width':1280,'height':720,'fps':25,'duration':8,'title':'自己的主题','text':'第一句话。第二句话。','brief':'当前内容要求','words':[]},ensure_ascii=False)+",'./output.mp4');\n")
        z.writestr('README.md','# '+style['name']+'\n\n原始动画来源：mg-styles-15，MIT，作者 Vincent Wei。固定版本及原文件哈希见 templates/'+sid+'/SOURCE.json。字体来源、授权及替代说明见 mg-upstream/FONT-SOURCES.json。\n\n环境：Node 24、FFmpeg、Chromium/Remotion 浏览器运行依赖、Noto CJK 字体。运行 npm ci，然后 npx remotion browser ensure；执行 node render-example.mjs 输出 output.mp4。\n\n直接使用原生 renderAt 源码的几何图形与运镜；时间轴按配音时长映射，跨比例完整保留构图并留边。受审查的文本槽位可适配内容；固定图形故事不随任意主题重画。3D 风格因上游缺少 Blender 序列帧，明确使用 catalog 中原版成片作为视觉素材，其内嵌文字不变。\n\n预览视频与效果图逐字节复用原仓库。实际源码渲染字体不承诺像素一致；不使用应用的简化图解作为替代。\n')
print(json.dumps({'source_packages':len(styles)}))
