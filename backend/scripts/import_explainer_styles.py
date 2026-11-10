"""Import the six selected reference cases. Does not run models or user tasks.

Production rules are curated independently of example stories. Media is only
used by the reference gallery; production never receives these video paths.
"""
import argparse
import hashlib
import json
from pathlib import Path
import shutil
import urllib.request

SPECS = [
    dict(id="explain-podcast", name="口播图解 · 人物小窗", slug="stokebuilder-356793",
         description="人物口播与全屏图解交替，按台词推进重点，图解画面保留人物小窗。",
         treatment="collage", motion="stepped", palette=["#18191C", "#303238", "#FAF8F2", "#D9D5CB", "#A5ADB8"],
         features="黑白炭灰编辑式排版、纸张纹理、打字标签、线稿标注；用图形演示关系，按原声关键词出现，避免连续堆砌文字卡。",
         method="""用当前口播的信息节点组织镜头：人物建立话题 → 全屏动态图解解释概念 → 回到人物承接。图解、实拍素材或屏幕截图全屏时，若启用人物，使用右下固定圆角或圆形人物小窗；为人物保留短边28%的空位。几段图解之间保留人物镜头的呼吸时间，不把全部口播铺满动效。
每镜先说明一个信息关系，再选择能演示它的物体、路径、连接或状态变化。纸张、线稿、半调网点和打字标签统一使用；全片共用字体、间距、留白和配色。不得只用标题加三张矩形卡替代解释。文字忠于当前原文，每个动作对齐当前台词，不跨未切断的音频句子。"""),
    dict(id="explain-timeline", name="素材融合 · 演变时间线", slug="retropunkai-277590", preview_start=10,
         description="用真实素材承接事实，以结构生长、阶段对照和时间线解释变化。",
         treatment="stroke", motion="draw", palette=["#101D2B", "#20364B", "#F0F5FA", "#64DCC7", "#FFBB77"],
         features="建筑式结构线、清楚的阶段节点和细线标注；结构从简到繁生长，时间线推动事件，真实素材与图解交替。",
         method="""先从当前文案提取阶段与因果，按起点 → 变化过程 → 当前结果组织；文案有明确年份才显示年份，无年份用原文阶段词，不编造年代。跨镜保留一致的主体位置、轮廓与时间线。
实际素材必须来自用户上传且与内容相关的文件，用real素材及evidence/visual-pip镜头展示；图片或视频和代码动效由后期组合，禁止在Canvas中用占位框冒充真实照片，不向模型画图代码传外部URL。没有真实素材时明确采用结构示意和阶段图解，不宣称已展示实拍。动效用轮廓搭建、部件出现、轨迹推进、前后对照解释变化。"""),
    dict(id="explain-ui-morph", name="UI 演示 · 连续变形", slug="verbove-268381", preview_start=4,
         description="围绕同一视觉主体连续变形，通过点击、输入和状态反馈解释操作流程。",
         treatment="morph", motion="spring", palette=["#101424", "#252D43", "#F5F7FF", "#8F9CFF", "#58DFC1"],
         features="一个持续存在的容器改变尺寸、圆角和位置，内容错峰进出；光标动作与状态反馈配合，微弱弹性和连续构图。",
         method="""以当前流程的输入、处理、反馈和结果为状态序列，同一轮廓从按钮变为输入容器、进度区、结果区。每场尽量用同一对象完成状态转换；容器先动，旧内容先离场，新内容后进入，避免文字相互覆盖。不能把简单切卡或每场淡入黑屏称为一镜到底。
界面文字与数据必须来自当前文案或明确上传的产品资料；不编造UI、用户量、收益或操作结果。缺少真实界面时只做清楚的流程示意。光标先抵达再点击，点击后产生可解释的反馈；用轻微弹性、拉伸指示线和流畅跟随表现连续性。跨场衔接保持主体落点一致；以实际说话时间驱动，不强套120BPM。"""),
    dict(id="explain-product", name="产品讲解 · 五段叙事", slug="alex-prompter-997524", preview_start=15,
         description="按问题、价值、操作、证据、收尾组织内容，适合工具和产品介绍。",
         treatment="solid", motion="spring", palette=["#F7F4EF", "#FFFFFF", "#202744", "#425CEB", "#F1845B"],
         features="醒目短标题、品牌强调色、清晰的主体图形、分步演示；一个场景一个论点，图形动作服务于解释。",
         method="""在内容允许时采用五段结构：用户遇到的问题 → 当前产品提供的价值 → 最多三步说明操作方法 → 文案已有的一项证据 → 品牌或观点收尾。已有完整文案保持原意和顺序；缺少证据或品牌时用原文总结，不凭空补充。
视觉主角随内容改变：问题用受阻的流程或对照，价值用关系变化，操作用对象状态推进，证据用真实数据/素材，结尾保持简洁。每场用大标题与少量标签，不把全文搬上屏幕。全片强调色、字体和留白统一，转场利用主体位置或相同形状承接。"""),
    dict(id="explain-storybook", name="绘本科普 · 故事演绎", slug="astrothewizard-618782", preview_start=10,
         description="把抽象知识转成一个主角的旅程，用比喻、过程和对照帮助理解。",
         treatment="sketch", motion="stepped", palette=["#F4E7CC", "#FFF8E9", "#2C3542", "#D58042", "#5B9EA2"],
         features="暖纸底、手绘线条、低饱和色块、简洁原创图形角色；轨迹和物体变化承担解释，画面有故事感。",
         method="""从当前主题选一个简单、可持续追踪的图形主角，例如信息点、物件或能量粒子，用出发 → 遇到障碍/机制 → 状态改变 → 抵达结果讲清一个核心问题。角色仅为示意，不把数字人重画成卡通，不挪用光子原例的太阳、数字或故事。
每镜一个视觉中心，过程必须能看出因果；用路径、尺度对比、累积、分流或重复试验表现机制。需要模拟时在现有有界绘图约束内使用确定性解析轨迹，不增加随机数或昂贵物理引擎。温和的表情、回环和视觉比喻服务知识，不用装饰粒子遮挡正文。没有资料支持的数值不出现。"""),
    dict(id="explain-chronicle", name="国风叙事 · 编年长卷", slug="dotey-927166", preview_start=20,
         video_url="https://video.twimg.com/amplify_video/2103961534720360448/vid/avc1/1280x720/QUw8Xd5S2z3bAu82.mp4",
         description="宣纸与黑金画面交替，以书法关键词、纹样和卷轴时间线串联章节。",
         treatment="stroke", motion="draw", palette=["#F2E8D3", "#FFF9EB", "#24211E", "#A8372D", "#AC8A45"],
         features="宣纸白描、玄底泥金、朱红印章；线稿逐笔展开、书法感关键词、卷轴时间尺与克制的传统纹样。",
         method="""将当前文案按年代、阶段或章节展开为长卷。只用原文出现的年代和名称；非历史主题使用原文阶段词，不强加朝代或假年份。每镜一个关键词配一幅能解释本段的线稿，避免纯文字幻灯片。
宣纸白描与深色金线按章节交替，统一朱红强调、墨色线条和金色细节。用曲线逐笔显现、卷轴揭示、章印落位完成视觉推进；章印文字也必须来自当前原文。时间尺只标已有日期，跨场保持连续位置。尊重实际口播语速，不自行生成音乐或要求未实现的鼓钟音效；人物小窗采用安静、清晰的边框并避开时间尺。"""),
]

COMMON = """共同执行要求：上述规则控制当前视频的导演稿、素材规划和专用动效，不复制案例的具体故事、品牌或事实。用户当前文案和已有素材优先。使用既有 Seedance 原声人物视频与实际字词时间，不新增 TTS，不重新生成重复人物，不变速人物视频。未启用人物时不要求人物窗。仅使用平台支持的图解、分屏、画中画、真实素材与确定性 Canvas 绘制；外部图片和视频由素材/合成通道处理，不在隔离绘图代码里联网加载。转场、图解出现和稳定阅读时间适配真实音频；不要照搬原例固定时长、节拍、音乐和音效。\n"""

def download(url, destination):
    if destination.exists() and destination.stat().st_size > 0:
        return
    request = urllib.request.Request(url, headers={"User-Agent": "Mozilla/5.0"})
    temp = destination.with_suffix(destination.suffix + ".part")
    try:
        with urllib.request.urlopen(request, timeout=60) as response, temp.open("wb") as out:
            shutil.copyfileobj(response, out)
        temp.replace(destination)
    finally:
        temp.unlink(missing_ok=True)

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("reference", type=Path)
    parser.add_argument("--media", action="store_true")
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[1] / "avatar-runtime"
    entries = {e["slug"]: e for e in json.loads((args.reference / "data/videos.json").read_text(encoding="utf-8"))}
    catalog = root / "catalog"
    catalog.mkdir(exist_ok=True)
    manifest = []
    for spec in SPECS:
        entry = entries[spec["slug"]]
        folder = root / "templates" / spec["id"]
        folder.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(args.reference / "prompts" / (spec["slug"] + ".md"), folder / "original-prompt.md")
        shutil.copyfile(args.reference / "LICENSE", folder / "REFERENCE-LICENSE.txt")
        (folder / "production.md").write_text(spec["method"] + "\n\n" + COMMON, encoding="utf-8")
        video = spec.get("video_url") or (("https://media.skillry.dev/opus-5-5/" + spec["slug"] + "/original.mp4") if entry["has_remake"] else None)
        source = dict(repository="https://github.com/yihui-dev/awesome-opus5-5-videos", slug=spec["slug"],
                      author=entry["author"], original_post=entry["post_url"], poster_url=entry["poster_url"],
                      video_url=video, preview_page=entry["skillry_url"], prompt_partial=entry["prompt_partial"], preview_start=spec.get("preview_start",0),
                      document_license="MIT", media_attribution="Original creator; media used only as attributed reference previews, not production assets.",
                      production="Curated method + current narration + current assets; no source-template or exact-reproduction claim.")
        if args.media:
            download(entry["poster_url"], catalog / (spec["id"] + "-reference.webp"))
            if video:
                download(video, catalog / (spec["id"] + "-reference.mp4"))
        for ext in ["webp", "mp4"]:
            media = catalog / (spec["id"] + "-reference." + ext)
            if media.exists():
                source[ext + "_sha256"] = hashlib.sha256(media.read_bytes()).hexdigest()
        (folder / "SOURCE.json").write_text(json.dumps(source, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        manifest.append(dict(id=spec["id"], name=spec["name"], version="explainer/1", source="awesome-opus5-5-videos",
                             production_mode="content-motion", description=spec["description"],
                             ratios=["9:16", "16:9", "1:1"], templates=["steps", "compare", "relationship", "highlight", "summary", "data"],
                             preview_kind="upstream-original" if video else "reference-poster", preview_start=spec.get("preview_start",0),
                             original_post=entry["post_url"], preview_page=entry["skillry_url"],
                             design={key: spec[key] for key in ["treatment", "motion", "palette", "features"]}))
        print(spec["id"], "reference-video" if video else "reference-poster", flush=True)
    (root / "explainer-styles.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")

if __name__ == "__main__":
    main()
