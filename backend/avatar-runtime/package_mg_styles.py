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
        z.writestr('README.md','# '+style['name']+'：原始案例参考包\n\n原始动画来源：mg-styles-15，MIT，作者 Vincent Wei。固定版本及原文件哈希见 templates/'+sid+'/SOURCE.json。字体来源、授权及替代说明见 mg-upstream/FONT-SOURCES.json。\n\n本包用于独立查看和复现原始案例，不是项目的生产渲染器。项目制作使用 content-motion：AI 根据当前口播文案和数字人画面生成动效方案，参考风格的配色、材质及运动语法，再逐帧渲染新的图形与构图。演示片的品牌、固定故事和视频不进入项目成片。\n\n参考包运行环境：Node 24、FFmpeg、Chromium/Remotion 浏览器运行依赖、Noto CJK 字体。运行 npm ci，然后 npx remotion browser ensure；执行 node render-example.mjs 输出原案例参考复现 output.mp4。\n\n参考复现保留原生 renderAt 几何与运镜，部分文字槽位可替换；固定图形故事不随主题重画。reference_mode 为 original-video 的案例直接播放 catalog 中原版成片。此路径仅用于参考包，项目生产不调用 renderNative。\n\n参考视频与效果图逐字节保留原仓库文件。源码复现因字体环境差异不承诺像素一致。\n')
print(json.dumps({'source_packages':len(styles)}))
