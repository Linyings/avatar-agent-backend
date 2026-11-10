# RunningHub 国际站可用模型清单（生成于 2026-11-09）

来源：`https://www.runninghub.ai/runninghub-api-doc-en/llms.txt` 及各接口 OpenAPI 文档（标准模型 API）。
已排除：ComfyUI 工作流式接口（参数为 `节点##字段`）、LoRA 版、已标记 Deprecated、IP 模板（周星驰）、spicy、3D、角色/元素上传类。价格文档未公开，以「模型对比」实测的 `thirdPartyConsumeMoney` 为准。

## 一、背景音乐 / 音效（步骤 2）

### 音乐与音频生成（13 个）

| # | 模型 | 接口路径 | 类型 | 关键参数 |
|---|---|---|---|---|
| 1 | mureka-v7.6/generate-bgm | `/openapi/v2/mureka-ai/mureka-v7.6/generate-bgm` | 纯音乐 BGM | prompt, instrumentalId, stream |
| 2 | minimax/music-2.5 | `/openapi/v2/rhart-audio/text-to-audio/music-2.5` | 带人声歌曲（需歌词） | prompt, lyrics, bitrate, sampleRate |
| 3 | minimax/music-2.6/text-to-instrumental | `/openapi/v2/minimax/music-2.6/text-to-instrumental` | 纯音乐 BGM | prompt, sampleRate, bitrate, format |
| 4 | minimax/music-2.6/text-to-music | `/openapi/v2/minimax/music-2.6/text-to-music` | 带人声歌曲（需歌词） | prompt, lyrics, sampleRate, bitrate, format, lyricsOptimizer |
| 5 | doubao-seed-audio-1.0 | `/openapi/v2/bytedance/doubao-seed-audio-1.0` | 多模态音频（含环境音/音效/对白） | text_prompt, speaker, audio_url, image_url, format, sample_rate, speech_rate, lo |
| 6 | mureka-v9/generate-bgm | `/openapi/v2/mureka-ai/mureka-v9/generate-bgm` | 纯音乐 BGM | prompt, instrumentalId, stream |
| 7 | mureka-v8/generate-bgm | `/openapi/v2/mureka-ai/mureka-v8/generate-bgm` | 纯音乐 BGM | prompt, instrumentalId, stream |
| 8 | suno-single-v5.5 | `/openapi/v2/rhart-audio/suno-v5.5/single` | 可纯音乐（make_instrumental） | title, description, make_instrumental |
| 9 | suno-custom-v5.5 | `/openapi/v2/rhart-audio/suno-v5.5/custom` | 带人声歌曲（需歌词） | title, prompt, tags |
| 10 | suno-single-v5 | `/openapi/v2/rhart-audio/suno-v5/single` | 可纯音乐（make_instrumental） | title, description, make_instrumental |
| 11 | suno-custom-v5 | `/openapi/v2/rhart-audio/suno-v5/custom` | 带人声歌曲（需歌词） | title, prompt, tags |
| 12 | suno-single-v4.5 | `/openapi/v2/rhart-audio/suno-v4.5/single` | 可纯音乐（make_instrumental） | title, description, make_instrumental |
| 13 | suno-custom-v4.5 | `/openapi/v2/rhart-audio/suno-v4.5/custom` | 带人声歌曲（需歌词） | title, prompt, tags |

## 二、图片（步骤 3）

### 文生图（35 个）

| # | 模型 | 接口路径 | 画幅 | 分辨率 |
|---|---|---|---|---|
| 1 | seedream-v4.5/text-to-image | `/openapi/v2/seedream-v4.5/text-to-image` | — | 2k/4k |
| 2 | seedream-v4/text-to-image | `/openapi/v2/seedream-v4/text-to-image` | — | 1k/2k/4k |
| 3 | seedream-v5-lite/text-to-image | `/openapi/v2/seedream-v5-lite/text-to-image` | — | 2k/3k |
| 4 | seedream-v5-pro/text-to-image | `/openapi/v2/seedream-v5-pro/text-to-image` | — | 1k/2k |
| 5 | seedream-v4.7/text-to-image | `/openapi/v2/seedream-v4.7/text-to-image` | — | 1k/2k/4k |
| 6 | grok-image/text-to-image/channel-low-price | `/openapi/v2/rhart-image-g/text-to-image` | 960x960/720x1280/1280x720/1168x784/784x1168 | — |
| 7 | qwen-image-3.0/text-to-image | `/openapi/v2/alibaba/qwen-image-3.0/text-to-image` | 1024*1024 | — |
| 8 | qwen-image-3.0-pro/text-to-image | `/openapi/v2/alibaba/qwen-image-3.0-pro/text-to-image` | 1024*1024 | — |
| 9 | qwen-image-2.0/text-to-image | `/openapi/v2/alibaba/qwen-image-2.0/text-to-image` | 1024*1024/1536*1536/768*1152/1024*1536/1152*768/1536*1024/960*1280/1080*1440/1280*960 | — |
| 10 | qwen-image-2.0-pro/text-to-image | `/openapi/v2/alibaba/qwen-image-2.0-pro/text-to-image` | 1024*1024/1536*1536/768*1152/1024*1536/1152*768/1536*1024/960*1280/1080*1440/1280*960 | — |
| 11 | wan-2.5-preview/text-to-image | `/openapi/v2/alibaba/wan-2.5-preview/text-to-image` | 1280*1280/1104*1472/1472*1104/960*1696/1696*960 | — |
| 12 | wan-2.7/text-to-image-pro | `/openapi/v2/alibaba/wan-2.7/text-to-image-pro` | — | — |
| 13 | wan-2.7/text-to-image | `/openapi/v2/alibaba/wan-2.7/text-to-image` | — | — |
| 14 | midjourney-text-to-image-v7 | `/openapi/v2/youchuan/text-to-image-v7` | 1:1/4:3/3:2/16:9/3:4/2:3/9:16 | 1/2/4 |
| 15 | midjourney-text-to-image-v8.2-preview | `/openapi/v2/youchuan/text-to-image-v82` | 1:1/4:3/3:2/16:9/3:4/2:3/9:16 | 1/4 |
| 16 | midjourney-text-to-image-niji7 | `/openapi/v2/youchuan/text-to-image-niji7` | 1:1/4:3/3:2/16:9/3:4/2:3/9:16 | — |
| 17 | midjourney-text-to-image-v61 | `/openapi/v2/youchuan/text-to-image-v61` | 1:1/4:3/3:2/16:9/3:4/2:3/9:16 | 0.5/1/2 |
| 18 | midjourney-text-to-image-niji6 | `/openapi/v2/youchuan/text-to-image-niji6` | 1:1/4:3/3:2/16:9/3:4/2:3/9:16 | 0.5/1/2 |
| 19 | nano-banana-pro/text-to-image-official-stable | `/openapi/v2/rhart-image-n-pro-official/text-to-image` | 1:1/3:2/2:3/3:4/4:3/4:5/5:4/9:16/16:9 | 1k/2k/4k |
| 20 | nano-banana/text-to-image-official-stable | `/openapi/v2/rhart-image-v1-official/text-to-image` | auto/1:1/16:9/9:16/4:3/3:4/3:2/2:3/5:4 | — |
| 21 | nano-banana-pro/text-to-image-ultra-official-stable | `/openapi/v2/rhart-image-n-pro-official/text-to-image-ultra` | 1:1/3:2/2:3/3:4/4:3/4:5/5:4/9:16/16:9 | 4k/8k |
| 22 | nano-banana2-gemini31flash/text-to-image-official-stable | `/openapi/v2/rhart-image-n-g31-flash-official/text-to-image` | 1:1/16:9/9:16/4:3/3:4/3:2/2:3/5:4/4:5 | 1k/2k/4k |
| 23 | nano-banana/text-to-image-channel-low-price | `/openapi/v2/rhart-image-v1/text-to-image` | auto/1:1/16:9/9:16/4:3/3:4/3:2/2:3/5:4 | — |
| 24 | nano-banana2-gemini31flash-lite/text-to-image-official-stable | `/openapi/v2/rhart-image-n-g31-flash-lite-official/text-to-image` | 1:1/16:9/9:16/4:3/3:4/3:2/2:3/5:4/4:5 | — |
| 25 | nano-banana-pro/text-to-image-channel-low-price | `/openapi/v2/rhart-image-n-pro/text-to-image` | 1:1/16:9/9:16/4:3/3:4/3:2/2:3/5:4/4:5 | 1k/2k/4k |
| 26 | nano-banana2-gemini31flash-lite/text-to-image-channel-low-price | `/openapi/v2/rhart-image-n-g31-flash-lite/text-to-image` | 1:1/16:9/9:16/4:3/3:4/3:2/2:3/5:4/4:5 | — |
| 27 | nano-banana2-gemini31flash/text-to-image-channel-low-price | `/openapi/v2/rhart-image-n-g31-flash/text-to-image` | 1:1/16:9/9:16/4:3/3:4/3:2/2:3/5:4/4:5 | 1k/2k/4k |
| 28 | gpt-image-1.5/text-to-image-official-stable | `/openapi/v2/rhart-image-g-1.5-official/text-to-image` | 1024*1024/1024*1536/1536*1024 | low/medium/high |
| 29 | gpt-image-2.0/text-to-image-channel-low-price | `/openapi/v2/rhart-image-g-2/text-to-image` | 1:1/3:2/2:3/5:4/4:5/16:9/9:16/21:9/3:4 | 1k/2k/4k |
| 30 | gpt-image-2/text-to-image-official-stable | `/openapi/v2/rhart-image-g-2-official/text-to-image` | 1:1/1:2/2:1/1:3/3:1/2:3/3:2/3:4/4:3 | 1k/2k/4k |
| 31 | xai/grok-imagine-image/text-to-image-official-stable | `/openapi/v2/rhart-image-x-official/text-to-image` | 2:1/20:9/16:9/4:3/3:2/1:1/2:3/3:4/9:16 | — |
| 32 | xai/rhart-imagine-image-quality/text-to-image-official-stable | `/openapi/v2/rhart-imagine-image-quality/text-to-image` | 1:1/16:9/9:16/4:3/3:4/3:2/2:3 | 1k/2k |
| 33 | bytedance/jimeng-4.6/text-to-image | `/openapi/v2/bytedance/jimeng-4.6/text-to-image` | — | — |
| 34 | Dola-Seedream-5.0-pro/text-to-image | `/openapi/v2/dola-Seedream-5.0-pro/text-to-image` | — | 1k/2k |
| 35 | seedream-v5-flash/text-to-image | `/openapi/v2/seedream-v5-flash/text-to-image` | — | 1k/1.5k/2k |

### 图生图 / 编辑 / 放大（41 个）

| # | 模型 | 接口路径 | 画幅 | 分辨率 |
|---|---|---|---|---|
| 1 | seedream-v4.5/image-to-image | `/openapi/v2/seedream-v4.5/image-to-image` | — | 2k/4k |
| 2 | seedream-v4/image-to-image | `/openapi/v2/seedream-v4/image-to-image` | — | 1k/2k/4k |
| 3 | seedream-v5-lite/image-to-image | `/openapi/v2/seedream-v5-lite/image-to-image` | — | 2k/3k |
| 4 | seedream-v5-pro/image-to-image | `/openapi/v2/seedream-v5-pro/image-to-image` | — | 1k/2k |
| 5 | seedream-v4.7/image-to-image | `/openapi/v2/seedream-v4.7/image-to-image` | — | 1k/2k/4k |
| 6 | grok-image/image-to-image/channel-low-price | `/openapi/v2/rhart-image-g/image-to-image` | — | — |
| 7 | qwen-image-3.0/image-edit | `/openapi/v2/alibaba/qwen-image-3.0/image-edit` | 1024*1024 | — |
| 8 | qwen-image-3.0-pro/image-edit | `/openapi/v2/alibaba/qwen-image-3.0-pro/image-edit` | 1024*1024 | — |
| 9 | qwen-image-2.0/image-edit | `/openapi/v2/alibaba/qwen-image-2.0/image-edit` | 1024*1024/1536*1536/768*1152/1024*1536/1152*768/1536*1024/960*1280/1080*1440/1280*960 | — |
| 10 | qwen-image-2.0-pro/image-edit | `/openapi/v2/alibaba/qwen-image-2.0-pro/image-edit` | 1024*1024/1536*1536/768*1152/1024*1536/1152*768/1536*1024/960*1280/1080*1440/1280*960 | — |
| 11 | wan-2.5-preview/image-to-image | `/openapi/v2/alibaba/wan-2.5-preview/image-to-image` | 1280*1280/1024*1024/800*1200/1200*800/960*1280/1280*960/720*1280/1280*720/1344*576 | — |
| 12 | midjourney-text-to-image-v8.1 | `/openapi/v2/youchuan/text-to-image-v81` | 1:1/4:3/3:2/16:9/3:4/2:3/9:16 | — |
| 13 | midjourney-text-to-image-v6 | `/openapi/v2/youchuan/text-to-image-v6` | 1:1/4:3/3:2/16:9/3:4/2:3/9:16 | — |
| 14 | nano-banana-pro/edit-official-stable | `/openapi/v2/rhart-image-n-pro-official/edit` | 1:1/3:2/2:3/3:4/4:3/4:5/5:4/9:16/16:9 | 1k/2k/4k |
| 15 | nano-banana/edit-official-stable | `/openapi/v2/rhart-image-v1-official/edit` | auto/1:1/16:9/9:16/4:3/3:4/3:2/2:3/5:4 | — |
| 16 | nano-banana-pro/edit-ultra-official-stable | `/openapi/v2/rhart-image-n-pro-official/edit-ultra` | 1:1/3:2/2:3/3:4/4:3/4:5/5:4/9:16/16:9 | 4k/8k |
| 17 | nano-banana2-gemini31flash/image-to-image-official-stable | `/openapi/v2/rhart-image-n-g31-flash-official/image-to-image` | 1:1/16:9/9:16/4:3/3:4/3:2/2:3/5:4/4:5 | 1k/2k/4k |
| 18 | nano-banana/edit-channel-low-price | `/openapi/v2/rhart-image-v1/edit` | auto/1:1/16:9/9:16/4:3/3:4/3:2/2:3/5:4 | — |
| 19 | nano-banana2-gemini31flash-lite/image-to-image-official-stable | `/openapi/v2/rhart-image-n-g31-flash-lite-official/image-to-image` | auto/1:1/16:9/9:16/4:3/3:4/3:2/2:3/5:4 | — |
| 20 | nano-banana2-gemini31flash-lite/image-to-image-channel-low-price | `/openapi/v2/rhart-image-n-g31-flash-lite/image-to-image` | 1:1/16:9/9:16/4:3/3:4/3:2/2:3/5:4/4:5 | — |
| 21 | nano-banana-pro/edit-channel-low-price | `/openapi/v2/rhart-image-n-pro/edit` | 1:1/16:9/9:16/4:3/3:4/3:2/2:3/5:4/4:5 | 1k/2k/4k |
| 22 | nano-banana2-gemini31flash/image-to-image-channel-low-price | `/openapi/v2/rhart-image-n-g31-flash/image-to-image` | 1:1/16:9/9:16/4:3/3:4/3:2/2:3/5:4/4:5 | 1k/2k/4k |
| 23 | gpt-image-1.5/image-to-image-official-stable | `/openapi/v2/rhart-image-g-1.5-official/image-to-image` | 1024*1024/1024*1536/1536*1024 | — |
| 24 | gpt-image-2.0/edit-channel-low-price | `/openapi/v2/rhart-image-g-2/image-to-image` | 1:1/2:3/3:2/4:5/5:4/4:3/3:4/16:9/9:16 | 1k/2k/4k |
| 25 | gpt-image-2/image-to-image-official-stable | `/openapi/v2/rhart-image-g-2-official/image-to-image` | 1:1/1:2/2:1/1:3/3:1/2:3/3:2/3:4/4:3 | 1k/2k/4k |
| 26 | xai/grok-imagine-image/edit-official-stable | `/openapi/v2/rhart-image-x-official/edit` | — | — |
| 27 | xai/rhart-imagine-image-quality/edit-official-stable | `/openapi/v2/rhart-imagine-image-quality/edit` | auto/1:1/16:9/9:16/4:3/3:4/3:2/2:3 | 1k/2k |
| 28 | bytedance/jimeng-4.6/image-to-image | `/openapi/v2/bytedance/jimeng-4.6/image-to-image` | — | — |
| 29 | Dola-Seedream-5.0-pro/image-to-image | `/openapi/v2/dola-Seedream-5.0-pro/image-to-image` | — | 1k/2k |
| 30 | topazlabs-image-gigapixel-standard-2 | `/openapi/v2/topazlabs/image-gigapixel-standard-2` | — | — |
| 31 | topazlabs-image-gigapixel-low-resolution-2 | `/openapi/v2/topazlabs/image-gigapixel-low-resolution-2` | — | — |
| 32 | wan-2.7/image-edit-pro | `/openapi/v2/alibaba/wan-2.7/image-edit-pro` | — | — |
| 33 | wan-2.7/image-edit | `/openapi/v2/alibaba/wan-2.7/image-edit` | — | — |
| 34 | topazlabs-image-gigapixel-text-and-shapes | `/openapi/v2/topazlabs/image-gigapixel-text-and-shapes` | — | — |
| 35 | topazlabs-image-gigapixel-high-fidelity-2 | `/openapi/v2/topazlabs/image-gigapixel-high-fidelity-2` | — | — |
| 36 | topazlabs-image-upscale-detail-faces | `/openapi/v2/topazlabs/image-upscale-detail-faces` | — | — |
| 37 | topazlabs-image-upscale-high-fidelity-v3 | `/openapi/v2/topazlabs/image-upscale-high-fidelity-v3` | — | — |
| 38 | topazlabs-image-upscale-transparent | `/openapi/v2/topazlabs/image-upscale-transparent` | — | — |
| 39 | topazlabs-image-gigapixel-art-and-cgi | `/openapi/v2/topazlabs/image-gigapixel-art-and-cgi` | — | — |
| 40 | seedream-v5-flash/image-to-image | `/openapi/v2/seedream-v5-flash/image-to-image` | — | 1k/1.5k/2k |
| 41 | bytedance/jimeng-seed3-tilesr/image-upscale | `/openapi/v2/bytedance/jimeng-seed3-tilesr/image-upscale` | — | 4k/8k |

## 三、AI 视频空镜（步骤 4）

### 文生视频（60 个）

| # | 模型 | 接口路径 | 时长(s) | 画幅 | 分辨率 | 原生音频 |
|---|---|---|---|---|---|---|
| 1 | Vidu-text-to-video-q3-turbo | `/openapi/v2/vidu/text-to-video-q3-turbo` | 1/2/3/4/5/6/7/8/9 | 4:3/3:4/16:9/9:16/1:1 | 540p/720p/1080p | true |
| 2 | Vidu-text-to-video-q3-pro | `/openapi/v2/vidu/text-to-video-q3-pro` | 1/2/3/4/5/6/7/8/9 | 4:3/3:4/16:9/9:16/1:1 | 360p/540p/720p/1080p | true |
| 3 | Vidu-text-to-video-q2 | `/openapi/v2/vidu/text-to-video` | 1/2/3/4/5/6/7/8/9 | 4:3/3:4/16:9/9:16/1:1 | 540p/720p/1080p | — |
| 4 | kling-v2.5-turbo-pro/text-to-video | `/openapi/v2/kling-v2.5-turbo-pro/text-to-video` | 5/10 | 1:1/16:9/9:16 | — | — |
| 5 | kling-v3.0-std-text-to-video | `/openapi/v2/kling-v3.0-std/text-to-video` | 3/4/5/6/7/8/9/10/11 | 1:1/16:9/9:16 | — | true |
| 6 | kling-video-o1/text-to-video | `/openapi/v2/kling-video-o1/text-to-video` | 5/10 | 1:1/9:16/16:9 | — | — |
| 7 | kling-v3.0-pro-text-to-video | `/openapi/v2/kling-v3.0-pro/text-to-video` | 3/4/5/6/7/8/9/10/11 | 1:1/16:9/9:16 | — | true |
| 8 | kling-v2.6-pro-text-to-video | `/openapi/v2/kling-v2.6-pro/text-to-video` | 5/10 | 1:1/16:9/9:16 | — | true/false |
| 9 | kling-video-o3-std-text-to-video | `/openapi/v2/kling-video-o3-std/text-to-video` | 5 | 1:1/16:9/9:16 | — | true |
| 10 | kling-video-o3-pro-text-to-video | `/openapi/v2/kling-video-o3-pro/text-to-video` | 5 | 1:1/16:9/9:16 | — | true |
| 11 | kling-v2.5-turbo-std/text-to-video | `/openapi/v2/kling-v2.5-turbo-std/text-to-video` | 5/10 | 1:1/16:9/9:16 | — | — |
| 12 | kling-v3-turbo-std-text-to-video | `/openapi/v2/kling-v3-turbo-std/text-to-video` | 3/4/5/6/7/8/9/10/11 | 1:1/16:9/9:16 | — | — |
| 13 | kling-v3-turbo-pro-text-to-video | `/openapi/v2/kling-v3-turbo-pro/text-to-video` | 3/4/5/6/7/8/9/10/11 | 1:1/16:9/9:16 | — | — |
| 14 | kling-v3-4k-text-to-video | `/openapi/v2/kling-v3-4k/text-to-video` | 3/4/5/6/7/8/9/10/11 | 16:9/9:16/1:1 | — | false |
| 15 | kling-video-o3-4k-text-to-video | `/openapi/v2/kling-video-o3-4k/text-to-video` | 5 | 16:9/9:16/1:1 | — | false |
| 16 | kling-v2.6-std-text-to-video | `/openapi/v2/kling-v2.6-std/text-to-video` | 5/10 | 1:1/16:9/9:16 | — | — |
| 17 | seedance-2.0/text-to-video | `/openapi/v2/rhart-video/sparkvideo-2.0/text-to-video` | -1/4/5/6/7/8/9/10/11 | adaptive/16:9/4:3/1:1/3:4/9:16/21:9 | 480p/720p/native1080p/native4k/1080p/2k/4k | true |
| 18 | seedance-2.5/text-to-video Token | `/openapi/v2/bytedance/seedance-2.5-token/text-to-video` | -1/4/5/6/7/8/9/10/11 | adaptive/16:9/4:3/1:1/3:4/9:16/21:9 | 480p/720p/native1080p/1080p/2k/4k | true |
| 19 | seedance-v1.5-pro-text-to-video-fast | `/openapi/v2/seedance-v1.5-pro/text-to-video-fast` | 4/5/6/7/8/9/10/11/12 | 16:9/9:16/4:3/3:4/1:1/21:9 | 720p/1080p | true/false |
| 20 | seedance-2.0-fast/text-to-video | `/openapi/v2/rhart-video/sparkvideo-2.0-fast/text-to-video` | -1/4/5/6/7/8/9/10/11 | adaptive/16:9/4:3/1:1/3:4/9:16/21:9 | 480p/720p/1080p/2k/4k | true |
| 21 | seedance-2.5-global/text-to-video Token | `/openapi/v2/bytedance/seedance-2.5-global-token/text-to-video` | -1/4/5/6/7/8/9/10/11 | adaptive/16:9/4:3/1:1/3:4/9:16/21:9 | 480p/720p/native1080p/1080p/2k/4k | true |
| 22 | seedance-v1.5-pro-text-to-video | `/openapi/v2/seedance-v1.5-pro/text-to-video` | 4/5/6/7/8/9/10/11/12 | 16:9/9:16/4:3/3:4/1:1/21:9 | 480p/720p/1080p | true/false |
| 23 | seedance-2.0-global-mini/text-to-video | `/openapi/v2/bytedance/seedance-2.0-global-mini/text-to-video` | -1/4/5/6/7/8/9/10/11 | adaptive/16:9/4:3/1:1/3:4/9:16/21:9 | 480p/720p/1080p/2k/4k | true |
| 24 | seedance-2.0-mini/text-to-video | `/openapi/v2/rhart-video/sparkvideo-2.0-mini/text-to-video` | -1/4/5/6/7/8/9/10/11 | adaptive/16:9/4:3/1:1/3:4/9:16/21:9 | 480p/720p/1080p/2k/4k | true |
| 25 | seedance-2.0-global-fast/text-to-video | `/openapi/v2/bytedance/seedance-2.0-global-fast/text-to-video` | -1/4/5/6/7/8/9/10/11 | adaptive/16:9/4:3/1:1/3:4/9:16/21:9 | 480p/720p/1080p/2k/4k | true |
| 26 | seedance-2.0-global/text-to-video | `/openapi/v2/bytedance/seedance-2.0-global/text-to-video` | -1/4/5/6/7/8/9/10/11 | adaptive/16:9/4:3/1:1/3:4/9:16/21:9 | 480p/720p/native1080p/native4k/1080p/2k/4k | true |
| 27 | hailuo-02-pro | `/openapi/v2/minimax/hailuo-02/pro` | 6 | — | — | — |
| 28 | hailuo-2.3-t2v-standard | `/openapi/v2/minimax/hailuo-2.3/t2v-standard` | 6/10 | — | — | — |
| 29 | hailuo-02-t2v-standard | `/openapi/v2/minimax/hailuo-02/t2v-standard` | 6/10 | — | — | — |
| 30 | hailuo-2.3-t2v-pro | `/openapi/v2/minimax/hailuo-2.3/t2v-pro` | — | — | — | — |
| 31 | hailuo-02-t2v-pro | `/openapi/v2/minimax/hailuo-02/t2v-pro` | — | — | — | — |
| 32 | google/veo3.1-pro/text-to-video-official-stable | `/openapi/v2/rhart-video-v3.1-pro-official/text-to-video` | 4/6/8 | 16:9/9:16 | 720p/1080p/4k | false |
| 33 | google/veo3.1-fast/text-to-video-official-stable | `/openapi/v2/rhart-video-v3.1-fast-official/text-to-video` | 4/6/8 | 16:9/9:16 | 720p/1080p/4k | true |
| 34 | google/veo3.1-fast/video-extend-official-stable | `/openapi/v2/rhart-video-v3.1-fast-official/video-extend` | — | — | 720p/1080p | — |
| 35 | google/veo3.1-pro/video-extend-official-stable | `/openapi/v2/rhart-video-v3.1-pro-official/video-extend` | — | — | 720p/1080p | — |
| 36 | google/veo3.1-lite/text-to-video-official-stable | `/openapi/v2/rhart-video-v3.1-lite-official/text-to-video` | 4/6/8 | 16:9/9:16 | 720p/1080p | — |
| 37 | google/veo3.1-lite/start-end-to-video-official-stable | `/openapi/v2/rhart-video-v3.1-lite-official/start-end-to-video` | — | 16:9/9:16 | 720p/1080p | — |
| 38 | google/veo3.1-fast/text-to-video-channel-low-price | `/openapi/v2/rhart-video-v3.1-fast/text-to-video` | 8 | 16:9/9:16 | 720p/1080p/4k | — |
| 39 | google/veo3.1-pro/text-to-video-channel-low-price | `/openapi/v2/rhart-video-v3.1-pro/text-to-video` | 8 | 16:9/9:16 | 720p/1080p/4k | — |
| 40 | xai/grok-imagine/text-to-video-official-stable | `/openapi/v2/rhart-video-g-official/text-to-video` | 6/10 | 16:9/9:16/1:1 | 720p/480p | — |
| 41 | xai/grok-imagine/text-to-video-channel-low-price-v1.5 | `/openapi/v2/rhart-video-g/text-to-video` | 6 | 2:3/3:2/1:1/16:9/9:16 | 720p/480p | — |
| 42 | xai / grok-imagine-video-v1.5 / text-to-video-official-stable | `/openapi/v2/rhart-video-g-official/text-to-video-v1.5` | 6 | 16:9/1:1/9:16/3:2/2:3 | 480p/720p/1080p | — |
| 43 | wan-2.7/text-to-video | `/openapi/v2/alibaba/wan-2.7/text-to-video` | 2/3/4/5/6/7/8/9/10 | 16:9/9:16/1:1/4:3/3:4 | 720P/1080P | — |
| 44 | wan-3.0/text-to-video | `/openapi/v2/alibaba/wan-3.0/text-to-video` | auto/2/3/4/5/6/7/8/9 | adaptive/16:9/4:3/1:1/3:4/9:16 | 480P/720P/1080P | true |
| 45 | wan-2.5-preview/text-to-video | `/openapi/v2/alibaba/wan-2.5-preview/text-to-video` | 5/10 | 832*480/480*832/624*624/1280*720/720*1280/960*960/1088*832/832*1088/1920*1080 | — | — |
| 46 | wan-3.0-prime/text-to-video | `/openapi/v2/alibaba/wan-3.0-prime/text-to-video` | auto/2/3/4/5/6/7/8/9 | adaptive/16:9/4:3/1:1/3:4/9:16 | 480P/720P/1080P | true |
| 47 | alibaba/wan-2.6/text-to-video | `/openapi/v2/alibaba/wan-2.6/text-to-video` | 5/10/15 | — | 1280*720/720*1280/1920*1080/1080*1920 | — |
| 48 | sora-2/text-to-video-official-stable | `/openapi/v2/rhart-video-s-official/text-to-video` | 4/8/12 | 720x1280/1280x720 | — | — |
| 49 | sora-2/text-to-video-channel-low-price | `/openapi/v2/rhart-video-s/text-to-video` | 10/15 | 9:16/16:9 | — | — |
| 50 | pixverse-v6/text-to-video | `/openapi/v2/pixverse-v6/text-to-video` | 5 | 16:9/4:3/1:1/3:4/9:16/2:3/3:2/21:9 | 360p/540p/720p/1080p | true |
| 51 | happyhorse-1.1/text-to-video | `/openapi/v2/alibaba/happyhorse-1.1/text-to-video` | 3/4/5/6/7/8/9/10/11 | 16:9/9:16/1:1/4:3/3:4/4:5/5:4/9:21/21:9 | 720p/1080p | — |
| 52 | happyhorse-1.0/text-to-video | `/openapi/v2/alibaba/happyhorse-1.0/text-to-video` | 3/4/5/6/7/8/9/10/11 | 16:9/9:16/1:1/4:3/3:4 | 720p/1080p | — |
| 53 | skyreels-v4/text-to-video-std | `/openapi/v2/skyreels-v4/text-to-video-std` | 5 | 16:9/9:16/1:1/4:3/3:4 | 480p/720p/1080p | true |
| 54 | skyreels-v4/text-to-video-fast | `/openapi/v2/skyreels-v4/text-to-video-fast` | 5 | 16:9/9:16/1:1/4:3/3:4 | 480p/720p/1080p | — |
| 55 | gemini-omni-flash/text-to-video/channel-low-price | `/openapi/v2/gemini-omni-flash/text-to-video` | 4/6/8/10 | 16:9/9:16 | 720p/1080p/4k | — |
| 56 | gemini-omni-1.1-flash/text-to-video/stable | `/openapi/v2/google/gemini-omni-1.1-flash/text-to-video` | 8 | 16:9/9:16 | 360p/720p/1080p/4k | — |
| 57 | MiniMax-H3 Text-to-Video | `/openapi/v2/minimax/hailuo-h3/text-to-video` | 5/6/7/8/9/10/11/12/13 | 21:9/16:9/4:3/1:1/3:4/9:16 | 2K/768P | — |
| 58 | MiniMax-H3-Max Text-to-Video | `/openapi/v2/minimax/hailuo-h3-max/text-to-video` | 5/6/7/8/9/10/11/12/13 | 21:9/16:9/4:3/1:1/3:4/9:16 | 480P/768P | — |
| 59 | flux-3-video/text-to-video | `/openapi/v2/rhart-video-flux3/text-to-video` | 5 | auto/21:9/2:1/16:9/4:3/1:1/3:4/9:16 | hd/fhd | true |
| 60 | minimax-h3-max-turbo/text-to-video | `/openapi/v2/minimax/h3-max-turbo/text-to-video` | 5/6/7/8/9/10/11/12/13 | 21:9/16:9/4:3/1:1/3:4/9:16 | 480p/768p | — |

### 图生视频（首帧 / 首尾帧）（79 个）

| # | 模型 | 接口路径 | 时长(s) | 画幅 | 分辨率 | 尾帧 |
|---|---|---|---|---|---|---|
| 1 | Vidu-image-to-video-q2-pro-fast | `/openapi/v2/vidu/image-to-video-q2-pro-fast` | 1/2/3/4/5/6/7/8/9 | — | 720p/1080p | — |
| 2 | Vidu-start-end-to-video-q3-turbo | `/openapi/v2/vidu/start-end-to-video-q3-turbo` | 1/2/3/4/5/6/7/8/9 | — | 540p/720p/1080p | 支持 |
| 3 | Vidu-image-to-video-q3-turbo | `/openapi/v2/vidu/image-to-video-q3-turbo` | 1/2/3/4/5/6/7/8/9 | — | 540p/720p/1080p | — |
| 4 | Vidu-start-end-to-video-q3-pro | `/openapi/v2/vidu/start-end-to-video-q3-pro` | 1/2/3/4/5/6/7/8/9 | — | 540p/720p/1080p | 支持 |
| 5 | Vidu-image-to-video-q3-pro | `/openapi/v2/vidu/image-to-video-q3-pro` | 1/2/3/4/5/6/7/8/9 | — | 360p/540p/720p/1080p/2k | — |
| 6 | Vidu-image-to-video-q2-turbo | `/openapi/v2/vidu/image-to-video-q2-turbo` | 1/2/3/4/5/6/7/8/9 | — | 540p/720p/1080p | — |
| 7 | Vidu-image-to-video-q2-pro | `/openapi/v2/vidu/image-to-video-q2-pro` | 1/2/3/4/5/6/7/8/9 | — | 540p/720p/1080p | — |
| 8 | Vidu-start-end-to-video-q2-turbo | `/openapi/v2/vidu/start-end-to-video-q2-turbo` | 1/2/3/4/5/6/7/8 | — | 540p/720p/1080p | 支持 |
| 9 | Vidu-start-end-to-video-q2-pro | `/openapi/v2/vidu/start-end-to-video-q2-pro` | 1/2/3/4/5/6/7/8 | — | 540p/720p/1080p | 支持 |
| 10 | Vidu-start-end-to-video-q2-pro-fast | `/openapi/v2/vidu/start-end-to-video-q2-pro-fast` | 1/2/3/4/5/6/7/8 | — | 720p/1080p | 支持 |
| 11 | kling-v2.5-turbo-pro/image-to-video | `/openapi/v2/kling-v2.5-turbo-pro/image-to-video` | 5/10 | — | — | 支持 |
| 12 | kling-v3.0-std-image-to-video | `/openapi/v2/kling-v3.0-std/image-to-video` | 3/4/5/6/7/8/9/10/11 | — | — | 支持 |
| 13 | kling-v2.5-turbo-std/image-to-video | `/openapi/v2/kling-v2.5-turbo-std/image-to-video` | 5/10 | — | — | — |
| 14 | kling-v3.0-pro-image-to-video | `/openapi/v2/kling-v3.0-pro/image-to-video` | 3/4/5/6/7/8/9/10/11 | — | — | 支持 |
| 15 | kling-video-o1/image-to-video | `/openapi/v2/kling-video-o1/image-to-video` | 5/10 | 1:1/9:16/16:9 | — | — |
| 16 | kling-video-o3-std-image-to-video | `/openapi/v2/kling-video-o3-std/image-to-video` | 5 | — | — | 支持 |
| 17 | kling-video-o1/start-to-end | `/openapi/v2/kling-video-o1/start-to-end` | 5/10 | 1:1/9:16/16:9 | — | 支持 |
| 18 | kling-video-o3-pro-image-to-video | `/openapi/v2/kling-video-o3-pro/image-to-video` | 5 | — | — | 支持 |
| 19 | kling-v3-turbo-std-image-to-video | `/openapi/v2/kling-v3-turbo-std/image-to-video` | 3/4/5/6/7/8/9/10/11 | — | — | — |
| 20 | kling-v2.6-pro-image-to-video | `/openapi/v2/kling-v2.6-pro/image-to-video` | 5/10 | — | — | — |
| 21 | kling-v3-turbo-pro-image-to-video | `/openapi/v2/kling-v3-turbo-pro/image-to-video` | 3/4/5/6/7/8/9/10/11 | — | — | — |
| 22 | kling-v2.6-std-image-to-video | `/openapi/v2/kling-v2.6-std/image-to-video` | 5/10 | — | — | — |
| 23 | kling-v3.0-4k-image-to-video | `/openapi/v2/kling-v3-4k/image-to-video` | 5 | — | — | 支持 |
| 24 | kling-video-o3-4k-image-to-video | `/openapi/v2/kling-video-o3-4k/image-to-video` | 5 | — | — | 支持 |
| 25 | seedance-2.0/image-to-video | `/openapi/v2/rhart-video/sparkvideo-2.0/image-to-video` | -1/4/5/6/7/8/9/10/11 | adaptive/16:9/4:3/1:1/3:4/9:16/21:9 | 480p/720p/native1080p/native4k/1080p/2k/4k | 支持 |
| 26 | seedance-v1.5-pro-image-to-video-fast | `/openapi/v2/seedance-v1.5-pro/image-to-video-fast` | 4/5/6/7/8/9/10/11/12 | 16:9/9:16/1:1/4:3/3:4/21:9/adaptive | 720p/1080p | 支持 |
| 27 | seedance-2.0-global/image-to-video | `/openapi/v2/bytedance/seedance-2.0-global/image-to-video` | -1/4/5/6/7/8/9/10/11 | adaptive/16:9/4:3/1:1/3:4/9:16/21:9 | 480p/720p/native1080p/native4k/1080p/2k/4k | 支持 |
| 28 | seedance-2.5/image-to-video Token | `/openapi/v2/bytedance/seedance-2.5-token/image-to-video` | -1/4/5/6/7/8/9/10/11 | adaptive | 480p/720p/native1080p/1080p/2k/4k | 支持 |
| 29 | seedance-2.0-fast/image-to-video | `/openapi/v2/rhart-video/sparkvideo-2.0-fast/image-to-video` | -1/4/5/6/7/8/9/10/11 | adaptive/16:9/4:3/1:1/3:4/9:16/21:9 | 480p/720p/1080p/2k/4k | 支持 |
| 30 | seedance-v1.5-pro-image-to-video | `/openapi/v2/seedance-v1.5-pro/image-to-video` | 4/5/6/7/8/9/10/11/12 | 16:9/9:16/1:1/4:3/3:4/21:9/adaptive | 480p/720p/1080p | 支持 |
| 31 | seedance-2.5-global/image-to-video Token | `/openapi/v2/bytedance/seedance-2.5-global-token/image-to-video` | -1/4/5/6/7/8/9/10/11 | adaptive | 480p/720p/native1080p/1080p/2k/4k | 支持 |
| 32 | seedance-2.0-global-mini/image-to-video | `/openapi/v2/bytedance/seedance-2.0-global-mini/image-to-video` | -1/4/5/6/7/8/9/10/11 | adaptive/16:9/4:3/1:1/3:4/9:16/21:9 | 480p/720p/1080p/2k/4k | 支持 |
| 33 | seedance-2.0-mini/image-to-video | `/openapi/v2/rhart-video/sparkvideo-2.0-mini/image-to-video` | -1/4/5/6/7/8/9/10/11 | adaptive/16:9/4:3/1:1/3:4/9:16/21:9 | 480p/720p/1080p/2k/4k | 支持 |
| 34 | seedance-2.0-global-fast/image-to-video | `/openapi/v2/bytedance/seedance-2.0-global-fast/image-to-video` | -1/4/5/6/7/8/9/10/11 | adaptive/16:9/4:3/1:1/3:4/9:16/21:9 | 480p/720p/1080p/2k/4k | 支持 |
| 35 | hailuo-02-fast | `/openapi/v2/minimax/hailuo-02/fast` | 6/10 | — | — | — |
| 36 | hailuo-02-standard | `/openapi/v2/minimax/hailuo-02/standard` | 6/10 | — | — | 支持 |
| 37 | hailuo-02-i2v-pro | `/openapi/v2/minimax/hailuo-02/i2v-pro` | — | — | — | 支持 |
| 38 | hailuo-2.3/i2v-pro | `/openapi/v2/minimax/hailuo-2.3/image-to-video-pro` | — | — | — | — |
| 39 | hailuo-2.3-fast-pro/image-to-video | `/openapi/v2/minimax/hailuo-2.3-fast-pro/image-to-video` | 6 | — | — | — |
| 40 | hailuo-2.3-fast/image-to-video | `/openapi/v2/minimax/hailuo-2.3-fast/image-to-video` | 6/10 | — | — | — |
| 41 | hailuo-2.3/i2v-standard | `/openapi/v2/minimax/hailuo-2.3/i2v-standard` | 6/10 | — | — | — |
| 42 | hailuo-02-i2v-standard | `/openapi/v2/minimax/hailuo-02/i2v-standard` | 6/10 | — | — | 支持 |
| 43 | midjourney-image-to-video | `/openapi/v2/youchuan/image-to-video` | — | — | 480p/720p | 支持 |
| 44 | google/veo3.1-pro/image-to-video-official-stable | `/openapi/v2/rhart-video-v3.1-pro-official/image-to-video` | 4/6/8 | 16:9/9:16 | 720p/1080p/4k | 支持 |
| 45 | google/veo3.1-pro/reference-to-video-official-stable | `/openapi/v2/rhart-video-v3.1-pro-official/reference-to-video` | — | — | 720p/1080p/4k | — |
| 46 | google/veo3.1-fast/image-to-video-official-stable | `/openapi/v2/rhart-video-v3.1-fast-official/image-to-video` | 4/6/8 | 16:9/9:16 | 720p/1080p/4k | 支持 |
| 47 | google/veo3.1-lite/image-to-video-official-stable | `/openapi/v2/rhart-video-v3.1-lite-official/image-to-video` | 4/6/8 | 16:9/9:16 | 720p/1080p | — |
| 48 | google/veo3.1-fast/image-to-video-channel-low-price | `/openapi/v2/rhart-video-v3.1-fast/image-to-video` | 8 | 16:9/9:16 | 720p/1080p/4k | — |
| 49 | google/veo3.1-fast/start-end-to-video-channel-low-price | `/openapi/v2/rhart-video-v3.1-fast/start-end-to-video` | 8 | 16:9/9:16 | 720p/1080p/4k | 支持 |
| 50 | google/veo3.1-pro/image-to-video-channel-low-price | `/openapi/v2/rhart-video-v3.1-pro/image-to-video` | 8 | 16:9/9:16 | 720p/1080p/4k | — |
| 51 | google/veo3.1-pro/start-end-to-video-channel-low-price | `/openapi/v2/rhart-video-v3.1-pro/start-end-to-video` | 8 | 16:9/9:16 | 720p/1080p/4k | 支持 |
| 52 | sora-2/image-to-video-realistic-official-stable | `/openapi/v2/rhart-video-s-official/image-to-video-realistic` | 4/8/12 | — | — | — |
| 53 | sora-2/image-to-video-official-stable | `/openapi/v2/rhart-video-s-official/image-to-video` | 4/8/12 | — | — | — |
| 54 | sora-2/text-to-video-pro-official-stable | `/openapi/v2/rhart-video-s-official/text-to-video-pro` | 4/8/12/16/20 | — | — | — |
| 55 | sora-2/image-to-video-pro-official-stable | `/openapi/v2/rhart-video-s-official/image-to-video-pro` | 4/8/12/16/20 | — | 720p/1080p | — |
| 56 | sora-2/image-to-video-channel-low-price | `/openapi/v2/rhart-video-s/image-to-video` | 10/15 | 9:16/16:9 | — | — |
| 57 | wan-2.6-reference-to-video-flash | `/openapi/v2/alibaba/wan-2.6/reference-to-video-flash` | 2/3/4/5/6/7/8/9/10 | — | — | — |
| 58 | wan-3.0/image-to-video | `/openapi/v2/alibaba/wan-3.0/image-to-video` | auto/2/3/4/5/6/7/8/9 | adaptive/16:9/4:3/1:1/3:4/9:16 | 480P/720P/1080P | 支持 |
| 59 | wan-3.0-prime/image-to-video | `/openapi/v2/alibaba/wan-3.0-prime/image-to-video` | auto/2/3/4/5/6/7/8/9 | adaptive/16:9/4:3/1:1/3:4/9:16 | 480P/720P/1080P | 支持 |
| 60 | wan-2.6-reference-to-video | `/openapi/v2/alibaba/wan-2.6/reference-to-video` | 2/3/4/5/6/7/8/9/10 | — | — | — |
| 61 | wan-2.6-image-to-video-flash | `/openapi/v2/alibaba/wan-2.6/image-to-video-flash` | 2/3/4/5/6/7/8/9/10 | — | 720p/1080p | — |
| 62 | wan-2.5-preview/image-to-video | `/openapi/v2/alibaba/wan-2.5-preview/image-to-video` | 5/10 | — | 480p/720p/1080p | — |
| 63 | wan-2.7/image-to-video | `/openapi/v2/alibaba/wan-2.7/image-to-video` | 2/3/4/5/6/7/8/9/10 | — | 720P/1080P | 支持 |
| 64 | xai/grok-imagine/image-to-video-channel-low-price-v1.5 | `/openapi/v2/rhart-video-g/image-to-video` | 6 | 2:3/3:2/1:1/16:9/9:16 | 720p/480p | — |
| 65 | xai/grok-imagine/image-to-video-official-stable | `/openapi/v2/rhart-video-g-official/image-to-video` | 6/10 | — | 720p/480p | — |
| 66 | xai/grok-imagine-video-v1.5/image-to-video-official-stable | `/openapi/v2/rhart-video-g-official/image-to-video-v1.5` | 6 | — | 480p/720p | — |
| 67 | alibaba/wan-2.6/image-to-video | `/openapi/v2/alibaba/wan-2.6/image-to-video` | 5/10/15 | — | 720p/1080p | — |
| 68 | pixverse-v6/image-to-video | `/openapi/v2/pixverse-v6/image-to-video` | 5 | — | 360p/540p/720p/1080p | — |
| 69 | happyhorse-1.0/image-to-video | `/openapi/v2/alibaba/happyhorse-1.0/image-to-video` | 3/4/5/6/7/8/9/10/11 | — | 720p/1080p | — |
| 70 | happyhorse-1.1/image-to-video | `/openapi/v2/alibaba/happyhorse-1.1/image-to-video` | 3/4/5/6/7/8/9/10/11 | — | 720p/1080p | — |
| 71 | skyreels-v4/image-to-video-std | `/openapi/v2/skyreels-v4/image-to-video-std` | 5 | — | 480p/720p/1080p | — |
| 72 | skyreels-v4/image-to-video-fast | `/openapi/v2/skyreels-v4/image-to-video-fast` | 5 | — | 480p/720p/1080p | — |
| 73 | gemini-omni-flash/image-to-video/channel-low-price | `/openapi/v2/gemini-omni-flash/image-to-video` | 4/6/8/10 | 16:9/9:16 | 720p/1080p/4k | — |
| 74 | gemini-omni-1.1-flash/image-to-video/stable | `/openapi/v2/google/gemini-omni-1.1-flash/image-to-video` | 8 | 16:9/9:16 | 360p/720p/1080p/4k | 支持 |
| 75 | MiniMax-H3 Image-to-Video (first/last frame) | `/openapi/v2/minimax/hailuo-h3/image-to-video` | 5/6/7/8/9/10/11/12/13 | — | 2K/768P | 支持 |
| 76 | MiniMax-H3-Max Image-to-Video (first/last frame) | `/openapi/v2/minimax/hailuo-h3-max/image-to-video` | 5/6/7/8/9/10/11/12/13 | — | 480P/768P | 支持 |
| 77 | flux-3-video/image-to-video | `/openapi/v2/rhart-video-flux3/image-to-video` | 5 | auto/21:9/2:1/16:9/4:3/1:1/3:4/9:16 | hd/fhd | — |
| 78 | minimax-h3-max-turbo/image-to-video | `/openapi/v2/minimax/h3-max-turbo/image-to-video` | 5/6/7/8/9/10/11/12/13 | — | 480p/768p | 支持 |
| 79 | vidu/q4-preview/image-to-video | `/openapi/v2/vidu/q4-preview/image-to-video` | 5 | — | 540p/720p/1080p/2k/4k | — |

### 参考生视频（多图/多模态参考）（31 个）

| # | 模型 | 接口路径 | 时长(s) | 画幅 | 分辨率 |
|---|---|---|---|---|---|
| 1 | seedance-2.0/multimodal-video | `/openapi/v2/rhart-video/sparkvideo-2.0/multimodal-video` | -1/4/5/6/7/8/9/10/11 | adaptive/16:9/4:3/1:1/3:4/9:16/21:9 | 480p/720p/native1080p/native4k/1080p/2k/4k |
| 2 | seedance-2.5/multimodal-video Token | `/openapi/v2/bytedance/seedance-2.5-token/multimodal-video` | -1/4/5/6/7/8/9/10/11 | adaptive/16:9/4:3/1:1/3:4/9:16/21:9 | 480p/720p/native1080p/1080p/2k/4k |
| 3 | seedance-v1-lite-reference-to-video | `/openapi/v2/seedance-v1-lite/reference-to-video` | 2/3/4/5/6/7/8/9/10 | 16:9/9:16/4:3/3:4/21:9/1:1 | 720p/480p |
| 4 | wan-3.0-reference-to-video | `/openapi/v2/alibaba/wan-3.0/reference-to-video` | auto/2/3/4/5/6/7/8/9 | adaptive/16:9/4:3/1:1/3:4/9:16 | 480P/720P/1080P |
| 5 | MiniMax-H3 Multimodal Reference-to-Video | `/openapi/v2/minimax/hailuo-h3/multimodal-to-video` | 5/6/7/8/9/10/11/12/13 | adaptive/21:9/16:9/4:3/1:1/3:4/9:16 | 2K/768P |
| 6 | wan-3.0-prime/reference-to-video | `/openapi/v2/alibaba/wan-3.0-prime/reference-to-video` | auto/2/3/4/5/6/7/8/9 | adaptive/16:9/4:3/1:1/3:4/9:16 | 480P/720P/1080P |
| 7 | seedance-2.0-fast/multimodal-video | `/openapi/v2/rhart-video/sparkvideo-2.0-fast/multimodal-video` | -1/4/5/6/7/8/9/10/11 | adaptive/16:9/4:3/1:1/3:4/9:16/21:9 | 480p/720p/1080p/2k/4k |
| 8 | seedance-2.5-global/multimodal-video Token | `/openapi/v2/bytedance/seedance-2.5-global-token/multimodal-video` | -1/4/5/6/7/8/9/10/11 | adaptive/16:9/4:3/1:1/3:4/9:16/21:9 | 480p/720p/native1080p/1080p/2k/4k |
| 9 | wan-2.7-reference-to-video | `/openapi/v2/alibaba/wan-2.7/reference-to-video` | 2/3/4/5/6/7/8/9/10 | 16:9/9:16/1:1/4:3/3:4 | 720P/1080P |
| 10 | happyhorse-1.0/reference-to-video | `/openapi/v2/alibaba/happyhorse-1.0/reference-to-video` | 3/4/5/6/7/8/9/10/11 | 16:9/9:16/3:4/4:3/1:1 | 720p/1080p |
| 11 | xai/grok-imagine-video/reference-to-video-official-stable | `/openapi/v2/rhart-video-g-official/reference-to-video` | 6/10 | — | 720p/480p |
| 12 | google/veo3.1-fast/reference-to-video-official-stable | `/openapi/v2/rhart-video-v3.1-fast-official/reference-to-video` | — | 16:9/9:16 | 720p/1080p |
| 13 | Vidu-reference-to-video-q2 | `/openapi/v2/vidu/reference-to-video-q2` | 1/2/3/4/5/6/7/8/9 | 16:9/9:16/4:3/3:4/1:1 | 540p/720p/1080p |
| 14 | Vidu-reference-to-video-q3 | `/openapi/v2/vidu/reference-to-video-q3` | 5 | 16:9/9:16/4:3/3:4/1:1/auto | 540p/720p/1080p |
| 15 | skyreels-v4/omni-reference-fast | `/openapi/v2/skyreels-v4/omni-reference-fast` | 5 | 16:9/9:16/1:1/4:3/3:4 | 480p/720p/1080p |
| 16 | happyhorse-1.1/reference-to-video | `/openapi/v2/alibaba/happyhorse-1.1/reference-to-video` | 3/4/5/6/7/8/9/10/11 | 16:9/9:16/3:4/4:3/1:1/4:5/5:4/9:21/21:9 | 720p/1080p |
| 17 | skyreels-v4/omni-reference-std | `/openapi/v2/skyreels-v4/omni-reference-std` | 5 | 16:9/9:16/1:1/4:3/3:4 | 480p/720p/1080p |
| 18 | Vidu-reference-to-video-q3-drama | `/openapi/v2/vidu/reference-to-video-q3-drama` | 8 | 16:9/9:16/4:3/3:4/1:1/auto | 1080p |
| 19 | kling-video-o1-std/refrence-to-video | `/openapi/v2/kling-video-o1-std/refrence-to-video` | 5/10 | 1:1/9:16/16:9 | — |
| 20 | seedance-2.0-global-mini/multimodal-video | `/openapi/v2/bytedance/seedance-2.0-global-mini/multimodal-video` | -1/4/5/6/7/8/9/10/11 | adaptive/16:9/4:3/1:1/3:4/9:16/21:9 | 480p/720p/1080p/2k/4k |
| 21 | kling-video-o3-std/reference-to-video | `/openapi/v2/kling-video-o3-std/reference-to-video` | 5 | 16:9/9:16/1:1 | — |
| 22 | seedance-2.0-mini/multimodal-video | `/openapi/v2/rhart-video/sparkvideo-2.0-mini/multimodal-video` | -1/4/5/6/7/8/9/10/11 | adaptive/16:9/4:3/1:1/3:4/9:16/21:9 | 480p/720p/1080p/2k/4k |
| 23 | kling-video-o3-pro/reference-to-video | `/openapi/v2/kling-video-o3-pro/reference-to-video` | 5 | 16:9/9:16/1:1 | — |
| 24 | Vidu-reference-to-video-q3-ad | `/openapi/v2/vidu/reference-to-video-q3-ad` | 5 | 16:9/9:16/4:3/3:4/1:1/auto | 720p/1080p |
| 25 | kling-video-o3-4k-reference-to-video | `/openapi/v2/kling-video-o3-4k/reference-to-video` | 5 | 16:9/9:16/1:1 | — |
| 26 | seedance-2.0-global/multimodal-video | `/openapi/v2/bytedance/seedance-2.0-global/multimodal-video` | -1/4/5/6/7/8/9/10/11 | adaptive/16:9/4:3/1:1/3:4/9:16/21:9 | 480p/720p/native1080p/native4k/1080p/2k/4k |
| 27 | Vidu-reference-to-video-q3-mix | `/openapi/v2/vidu/reference-to-video-q3-mix` | 5 | 16:9/9:16/4:3/3:4/1:1/auto | 720p/1080p |
| 28 | seedance-2.0-global-fast/multimodal-video | `/openapi/v2/bytedance/seedance-2.0-global-fast/multimodal-video` | -1/4/5/6/7/8/9/10/11 | adaptive/16:9/4:3/1:1/3:4/9:16/21:9 | 480p/720p/1080p/2k/4k |
| 29 | xai/grok-imagine-video-v1.5/reference-to-video-official-stable | `/openapi/v2/rhart-video-g-official/reference-to-video-v1.5` | 6 | 16:9/1:1/9:16/3:2/2:3 | 480p/720p |
| 30 | gemini-omni-1.1-flash/reference-to-video/stable | `/openapi/v2/google/gemini-omni-1.1-flash/reference-to-video` | 8 | 16:9/9:16 | 360p/720p/1080p/4k |
| 31 | vidu/q4-preview/reference-to-video | `/openapi/v2/vidu/q4-preview/reference-to-video` | 5 | 16:9/9:16/1:1/4:3/3:4 | 540p/720p/1080p/2k/4k |

### 视频增强（超分 / 插帧 / 降噪，可选后处理）（11 个）

| # | 模型 | 接口路径 | 参数 |
|---|---|---|---|
| 1 | topazlabs-video-frame-interpolation | `/openapi/v2/topazlabs/video-frame-interpolation` | videoUrl, outputWidth, outputHeight, model, outputFrameRate |
| 2 | topazlabs-video-astra | `/openapi/v2/topazlabs/video-astra` | videoUrl, outputWidth, outputHeight, model |
| 3 | topazlabs-video-starlight | `/openapi/v2/topazlabs/video-starlight` | videoUrl, outputWidth, outputHeight, model |
| 4 | topazlabs-video-denoise | `/openapi/v2/topazlabs/video-denoise` | videoUrl, outputWidth, outputHeight, model |
| 5 | rh-video-fps-increaser | `/openapi/v2/rhart-video/video-fps-increaser` | videoUrl |
| 6 | rh-video-upscaler | `/openapi/v2/rhart-video/video-upscaler` | videoUrl, targetResolution |
| 7 | volc-enhance-fast/video | `/openapi/v2/volc-enhance-fast/video` | videoUrl, resolution, resolutionLimit, fps, bitrateLevel, bitrate |
| 8 | volc-enhance/video | `/openapi/v2/volc-enhance/video` | videoUrl, mode, scene, enhanceStyle, resolution, resolutionLimit, fps, |
| 9 | volc-enhance-generative/video | `/openapi/v2/volc-enhance-generative/video` | videoUrl, resolution, fps, bitrateLevel, bitrate |
| 10 | topazlabs-video-proteus | `/openapi/v2/topazlabs/video-proteus` | videoUrl, outputWidth, outputHeight, model |
| 11 | DetailX/video-super-resolution | `/openapi/v2/arklin/video-super-resolution` | videoUrl, mode, resolution, tarfps |

### 数字人（可作为 F06 对比项）（2 个）

| # | 模型 | 接口路径 | 输入 |
|---|---|---|---|
| 1 | kling-v2-ai-avatar-standard/image-audio-to-video | `/openapi/v2/kling-v2-ai-avatar-standard/image-audio-to-video` | imageUrl, audioUrl |
| 2 | kling-v2-ai-avatar-pro/image-audio-to-video | `/openapi/v2/kling-v2-ai-avatar-pro/image-audio-to-video` | imageUrl, audioUrl |
