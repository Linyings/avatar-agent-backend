package avatar

import "strings"

// SpeechModel is one RunningHub standard text-to-speech API with preset voices.
// Voice lists come from the RunningHub API schemas (runninghub.ai, checked
// 2026-11-09). Doubao's schema has no enum, so its list is the official Seed-TTS
// 2.0 general-purpose voices; role-play and IP character voices are omitted.
type SpeechModel struct {
	ID            string
	Name          string
	DefaultVoice  string
	SupportsSpeed bool
	Voices        []SpeechVoice
	// Family groups versions of one provider that share the same preset
	// voices, so a preview recorded on one version can represent the others.
	Family string
}

// SpeechModelFamily lists registered models of the same family, model first.
func SpeechModelFamily(model string) []string {
	m, ok := FindSpeechModel(model)
	if !ok {
		return nil
	}
	out := []string{m.ID}
	for _, other := range speechModels {
		if other.ID != m.ID && m.Family != "" && other.Family == m.Family {
			out = append(out, other.ID)
		}
	}
	return out
}

func (m SpeechModel) HasVoice(id string) bool {
	for _, v := range m.Voices {
		if v.ID == id {
			return true
		}
	}
	return false
}

var doubaoSystemVoices = []SpeechVoice{
	{"zh_male_shaonianzixin_uranus_bigtts", "少年梓辛 · 清澈少年男声"},
	{"zh_female_vv_uranus_bigtts", "Vivi 2.0 · 活力女声"},
	{"zh_female_xiaohe_uranus_bigtts", "小何 · 柔和轻熟女声"},
	{"zh_male_m191_uranus_bigtts", "云舟 · 温暖叙事男声"},
	{"zh_male_taocheng_uranus_bigtts", "小天 · 儒雅商务男声"},
	{"zh_male_liufei_uranus_bigtts", "刘飞 · 沉稳大叔"},
	{"zh_female_sophie_uranus_bigtts", "魅力苏菲 · 知性御姐"},
	{"zh_female_qingxinnvsheng_uranus_bigtts", "清新女声"},
	{"zh_female_cancan_uranus_bigtts", "知性灿灿"},
	{"zh_female_tianmeixiaoyuan_uranus_bigtts", "甜美小源"},
	{"zh_female_shuangkuaisisi_uranus_bigtts", "爽快思思 · 干练快语速"},
	{"zh_female_linjianvhai_uranus_bigtts", "邻家女孩"},
	{"zh_female_kefunvsheng_uranus_bigtts", "暖阳女声"},
	{"zh_female_liuchangnv_uranus_bigtts", "流畅女声"},
	{"zh_male_ruyayichen_uranus_bigtts", "儒雅逸辰"},
	{"zh_female_wenroumama_uranus_bigtts", "温柔妈妈"},
	{"zh_male_jieshuoxiaoming_uranus_bigtts", "解说小明"},
	{"zh_female_tvbnv_uranus_bigtts", "TVB 女声 · 港风"},
	{"zh_male_yizhipiannan_uranus_bigtts", "译制片男声"},
	{"zh_male_linjiananhai_uranus_bigtts", "邻家男孩"},
	{"zh_male_ruyaqingnian_uranus_bigtts", "儒雅青年"},
	{"zh_male_wennuanahu_uranus_bigtts", "温暖阿虎"},
	{"zh_female_gaolengyujie_uranus_bigtts", "高冷御姐"},
	{"zh_female_wenroushunv_uranus_bigtts", "温柔淑女"},
	{"zh_male_huolixiaoge_uranus_bigtts", "活力小哥"},
	{"zh_male_baqiqingshu_uranus_bigtts", "霸气青叔"},
	{"zh_male_xuanyijieshuo_uranus_bigtts", "悬疑解说"},
	{"zh_male_cixingjieshuonan_uranus_bigtts", "磁性解说男声"},
	{"zh_female_kailangjiejie_uranus_bigtts", "开朗姐姐"},
	{"zh_male_shenyeboke_uranus_bigtts", "深夜播客"},
	{"zh_female_zhixingnv_uranus_bigtts", "知性女声"},
	{"zh_male_yuanboxiaoshu_uranus_bigtts", "渊博小叔"},
	{"zh_male_yangguangqingnian_uranus_bigtts", "阳光青年"},
	{"zh_male_dongfanghaoran_uranus_bigtts", "东方浩然 · 庄重正气"},
	{"zh_male_guanggaojieshuo_uranus_bigtts", "广告解说"},
}

var qwenFlashVoices = []SpeechVoice{
	{"Cherry", "芊悦 · 阳光亲切女声"}, {"Serena", "苏瑶 · 温柔女声"}, {"Ethan", "晨煦 · 阳光男声"},
	{"Chelsie", "千雪 · 二次元女声"}, {"Momo", "茉兔 · 撒娇搞怪女声"}, {"Vivian", "十三 · 可爱小暴躁"},
	{"Moon", "月白 · 率性帅气男声"}, {"Maia", "四月 · 知性温柔女声"}, {"Kai", "凯 · 舒缓男声"},
	{"Nofish", "不吃鱼 · 设计师男声"}, {"Bella", "萌宝 · 小萝莉"}, {"Eldric Sage", "沧明子 · 睿智老者"},
	{"Mia", "乖小妹 · 温顺女声"}, {"Mochi", "沙小弥 · 聪慧童声"}, {"Bellona", "燕铮莺 · 洪亮女声"},
	{"Vincent", "田叔 · 沙哑烟嗓"}, {"Bunny", "萌小姬 · 萌系少女"}, {"Neil", "阿闻 · 新闻主播男声"},
	{"Elias", "墨讲师 · 知识讲解女声"}, {"Arthur", "徐大爷 · 质朴老者"}, {"Nini", "邻家妹妹 · 甜软女声"},
	{"Seren", "小婉 · 舒缓助眠女声"}, {"Pip", "顽屁小孩 · 调皮童声"}, {"Stella", "少女阿月 · 甜美少女"},
	{"Jennifer", "詹妮弗 · 美式英语女声"}, {"Ryan", "甜茶 · 戏感男声"}, {"Katerina", "卡捷琳娜 · 御姐"},
	{"Aiden", "艾登 · 美式英语男声"}, {"Bodega", "博德加 · 西班牙大叔"}, {"Sonrisa", "索尼莎 · 拉美大姐"},
	{"Alek", "阿列克 · 俄式男声"}, {"Dolce", "多尔切 · 意大利大叔"}, {"Sohee", "素熙 · 韩国欧尼"},
	{"Ono Anna", "小野杏 · 日式少女"}, {"Lenn", "莱恩 · 德国青年"}, {"Emilien", "埃米尔安 · 法国男声"},
	{"Andre", "安德雷 · 磁性沉稳男声"}, {"Radio Gol", "拉迪奥·戈尔 · 足球解说"},
	{"Jada", "阿珍 · 上海话女声"}, {"Dylan", "晓东 · 北京话男声"}, {"Li", "老李 · 南京话男声"},
	{"Marcus", "秦川 · 陕西话男声"}, {"Roy", "阿杰 · 闽南腔男声"}, {"Peter", "李彼得 · 天津话男声"},
	{"Sunny", "晴儿 · 四川话女声"}, {"Eric", "程川 · 四川话男声"}, {"Rocky", "阿强 · 粤语男声"},
	{"Kiki", "阿清 · 粤语女声"},
}

// Kling's Chinese presets; voiceLanguage is always zh for these IDs.
var klingVoices = []SpeechVoice{
	{"genshin_vindi2", "阳光少年"}, {"zhinen_xuesheng", "懂事小弟"}, {"tiyuxi_xuedi", "运动少年"},
	{"ai_shatang", "青春少女"}, {"genshin_klee2", "温柔小妹"}, {"genshin_kirara", "元气少女"},
	{"ai_kaiya", "阳光男生"}, {"tiexin_nanyou", "幽默小哥"}, {"ai_chenjiahao_712", "文艺小哥"},
	{"girlfriend_1_speech02", "甜美邻家"}, {"chat1_female_new-3", "温柔姐姐"}, {"girlfriend_2_speech02", "职场女青"},
	{"cartoon-boy-07", "活泼男童"}, {"cartoon-girl-01", "俏皮女童"}, {"ai_huangyaoshi_712", "稳重老爸"},
	{"you_pingjing", "温柔妈妈"}, {"ai_laoguowang_712", "严肃上司"}, {"chengshu_jiejie", "优雅贵妇"},
	{"zhuxi_speech02", "慈祥爷爷"}, {"uk_oldman3", "唠叨爷爷"}, {"laopopo_speech02", "唠叨奶奶"},
	{"heainainai_speech02", "和蔼奶奶"}, {"dongbeilaotie_speech02", "东北老铁"}, {"chongqingxiaohuo_speech02", "重庆小伙"},
	{"chuanmeizi_speech02", "四川妹子"}, {"chaoshandashu_speech02", "潮汕大叔"}, {"ai_taiwan_man2_speech02", "台湾男生"},
	{"xianzhanggui_speech02", "西安掌柜"}, {"tianjinjiejie_speech02", "天津姐姐"}, {"diyinnansang_DB_CN_M_04-v2", "新闻播报男"},
	{"yizhipiannan-v1", "译制片男"}, {"guanxiaofang-v2", "元气少女 · 二号"}, {"tianmeixuemei-v1", "撒娇女友"},
	{"daopianyansang-v1", "刀片烟嗓"}, {"mengwa-v1", "乖巧正太"},
}

func miniMaxModel(id, name string) SpeechModel {
	return SpeechModel{ID: "minimax/" + id, Name: name, DefaultVoice: "Wise_Woman", SupportsSpeed: true, Voices: miniMaxSystemVoices, Family: "minimax"}
}

var speechModels = []SpeechModel{
	miniMaxModel("speech-2.8-hd", "MiniMax Speech 2.8 HD"),
	miniMaxModel("speech-2.8-turbo", "MiniMax Speech 2.8 Turbo"),
	miniMaxModel("speech-2.6-hd", "MiniMax Speech 2.6 HD"),
	miniMaxModel("speech-2.6-turbo", "MiniMax Speech 2.6 Turbo"),
	miniMaxModel("speech-02-hd", "MiniMax Speech-02 HD"),
	miniMaxModel("speech-02-turbo", "MiniMax Speech-02 Turbo"),
	{ID: doubaoTTSModel, Name: "豆包语音合成 2.0", DefaultVoice: "zh_male_shaonianzixin_uranus_bigtts", SupportsSpeed: true, Voices: doubaoSystemVoices},
	{ID: "alibaba/qwen3-tts-flash", Name: "千问 Qwen3-TTS Flash", DefaultVoice: "Cherry", Voices: qwenFlashVoices, Family: "qwen3-tts"},
	{ID: "alibaba/qwen3-tts-instruct-flash", Name: "千问 Qwen3-TTS Instruct Flash", DefaultVoice: "Cherry", Voices: qwenFlashVoices[:24], Family: "qwen3-tts"},
	{ID: "kling-lip-sync/tts", Name: "可灵 Kling TTS", DefaultVoice: "genshin_vindi2", SupportsSpeed: true, Voices: klingVoices},
}

// SpeechModels returns copies so callers cannot mutate the registry.
func SpeechModels() []SpeechModel {
	out := make([]SpeechModel, len(speechModels))
	for i, m := range speechModels {
		m.Voices = append([]SpeechVoice(nil), m.Voices...)
		out[i] = m
	}
	return out
}

func FindSpeechModel(id string) (SpeechModel, bool) {
	id = strings.TrimSpace(id)
	if strings.HasPrefix(id, "bytedance/") {
		id = strings.TrimPrefix(id, "bytedance/")
	}
	for _, m := range SpeechModels() {
		if m.ID == id {
			return m, true
		}
	}
	return SpeechModel{}, false
}
