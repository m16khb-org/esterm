package main

import (
	"os"
	"strings"
)

// langID is the UI language. English is the default so scripts and tests keep the strings
// they already know.
type langID int

const (
	langEN langID = iota
	langKO
	langZH
)

var currentLang = langEN
var langNames = [3]string{"English", "한국어", "中文"}

func parseLang(s string) (langID, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "en", "en-us", "en-gb", "english":
		return langEN, true
	case "ko", "kr", "ko-kr", "korean", "한국어":
		return langKO, true
	case "zh", "zh-cn", "zh-hans", "cn", "chinese", "中文":
		return langZH, true
	}
	return langEN, false
}

// tr returns the message for key in the current language, falling back to English.
func tr(key string) string {
	t, ok := messages[key]
	if !ok {
		return key
	}
	if t[currentLang] != "" {
		return t[currentLang]
	}
	return t[langEN]
}

// setLanguage applies a language and refreshes the values that are captured at startup.
func setLanguage(l langID) {
	currentLang = l
	refreshAppTabs()
}

// initLanguage applies ESTERM_LANG, then -lang/--lang from the command line, and returns
// the arguments with the language flag removed.
func initLanguage(args []string) []string {
	if v := os.Getenv("ESTERM_LANG"); v != "" {
		if l, ok := parseLang(v); ok {
			setLanguage(l)
		}
	}
	out := args[:0]
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-lang" || a == "--lang" {
			if i+1 < len(args) {
				if l, ok := parseLang(args[i+1]); ok {
					setLanguage(l)
				}
				i++
			}
			continue
		}
		if strings.HasPrefix(a, "-lang=") || strings.HasPrefix(a, "--lang=") {
			if l, ok := parseLang(a[strings.IndexByte(a, '=')+1:]); ok {
				setLanguage(l)
			}
			continue
		}
		out = append(out, a)
	}
	return out
}

// messages are indexed by langEN, langKO and langZH. Keep English identical to the old
// strings so default behaviour and tests do not change.
var messages = map[string][3]string{
	"language":          {"Language", "언어", "语言"},
	"lang_hint":         {"English · 한국어 · 中文", "English · 한국어 · 中文", "English · 한국어 · 中文"},
	"leagues":           {"Leagues", "리그", "联赛"},
	"matches":           {"Matches", "경기", "比赛"},
	"games":             {"Games", "세트", "对局"},
	"live":              {"Live", "라이브", "直播"},
	"upcoming":          {"Upcoming", "예정", "即将开始"},
	"recent":            {"Recent", "최근", "最近"},
	"replay":            {"Replay", "다시 보기", "回放"},
	"options":           {"Options", "옵션", "选项"},
	"keybindings":       {"Keybindings", "키 도움말", "快捷键"},
	"minimap_drawing":   {"Minimap drawing", "미니맵 그리기", "小地图绘制"},
	"video_sync":        {"Broadcast video sync", "방송 영상 동기화", "直播视频同步"},
	"poll_interval":     {"Poll interval", "폴링 간격", "轮询间隔"},
	"left_column":       {"Left column in board view", "보드 왼쪽 열", "看板左侧栏"},
	"desc_drawing":      {"auto · kitty (pixel icons) · blocks (half-blocks) · text (braille)", "auto · kitty(픽셀) · blocks(반블록) · text(점자)", "auto · kitty（像素）· blocks（半块）· text（盲文）"},
	"desc_video":        {"decode the broadcast minimap (needs yt-dlp + ffmpeg)", "방송 미니맵을 디코드합니다 (yt-dlp + ffmpeg 필요)", "解码直播小地图（需要 yt-dlp + ffmpeg）"},
	"desc_poll":         {"how often the live feed is fetched", "라이브 피드를 가져오는 주기", "拉取实时数据的间隔"},
	"desc_left":         {"hide the lists while a board has the focus", "보드에 포커스가 있을 때 목록을 숨깁니다", "看板获得焦点时隐藏列表"},
	"on":                {"on", "켜짐", "开"},
	"off":               {"off", "꺼짐", "关"},
	"shown":             {"shown", "표시", "显示"},
	"hidden":            {"hidden", "숨김", "隐藏"},
	"loading_game":      {"loading game %d…", "게임 %d 불러오는 중…", "正在加载第 %d 局…"},
	"loading":           {"loading…", "불러오는 중…", "加载中…"},
	"opening_live":      {"opening live board…", "라이브 보드 여는 중…", "正在打开直播看板…"},
	"no_matches":        {"no %s matches", "%s 경기가 없습니다", "没有%s比赛"},
	"preview":           {"Preview", "미리보기", "预览"},
	"count_of":          {"%d of %d", "%d / %d", "%d / %d"},
	"select_match":      {"select a match", "경기를 선택하세요", "请选择比赛"},
	"narrow_board":      {"terminal too narrow for the board", "보드를 표시하기에 터미널이 좁습니다", "终端太窄，无法显示看板"},
	"no_more_matches":   {"no more matches", "더 이상 경기가 없습니다", "没有更多比赛"},
	"game_not_started":  {"game %d has not started", "게임 %d은 아직 시작하지 않았습니다", "第 %d 局尚未开始"},
	"starts_at":         {"not started yet · starts %s", "아직 시작하지 않았습니다 · %s 시작", "尚未开始 · %s 开始"},
	"cannot_open":       {"cannot open: %s", "열 수 없습니다: %s", "无法打开：%s"},
	"no_match_selected": {"no match selected", "선택한 경기가 없습니다", "未选择比赛"},
	"error_prefix":      {"error: ", "오류: ", "错误："},
	"done_state":        {"done", "완료", "已完成"},
	"not_played":        {"not played", "미진행", "未进行"},
	"no_vod":            {"no VOD", "VOD 없음", "无 VOD"},
	"pick_league":       {"pick a league on the left", "왼쪽에서 리그를 선택하세요", "请在左侧选择联赛"},
	"pick_match":        {"pick a match: 2 / enter", "경기 선택: 2 / enter", "选择比赛：2 / enter"},
	"league_label":      {"league %s", "리그 %s", "联赛 %s"},
	"match_label":       {"Match", "경기", "比赛"},
	"completed_state":   {"completed", "종료", "已结束"},
	"score_label":       {"Score", "점수", "比分"},
	"start_label":       {"Start", "시작", "开始"},
	"vods_label":        {"VODs", "VOD", "VOD"},
	"vods_count":        {"%d of %d played games have an official VOD", "공식 VOD %d/%d경기", "官方 VOD %d/%d 场"},
	"enter_live":        {"open the live board (scoreboard + broadcast minimap)", "라이브 보드 열기 (점수판 + 방송 미니맵)", "打开直播看板（记分板 + 直播小地图）"},
	"enter_replay":      {"replay game %d (scoreboard + VOD minimap, space ←/→ +/-)", "게임 %d 다시 보기 (점수판 + VOD 미니맵, space ←/→ +/-)", "回放第 %d 局（记分板 + VOD 小地图，space ←/→ +/-）"},
	"enter_upcoming":    {"nothing yet: the match has not started", "아직 없습니다: 경기가 시작하지 않았습니다", "暂无内容：比赛尚未开始"},
	"player":            {"PLAYER", "선수", "选手"},
	"champion":          {"CHAMP", "챔피언", "英雄"},
	"kills":             {"KILLS", "킬", "击杀"},
	"gold":              {"GOLD", "골드", "金币"},
	"dragon":            {"Dragon", "용", "小龙"},
	"baron":             {"Baron", "바론", "大龙"},
	"tower":             {"TOWER", "타워", "防御塔"},
	"inhibitor":         {"INHIB", "억제기", "兵营"},
	"soul":              {"Soul", "소울", "龙魂"},
	"soul_point":        {"Soul point", "소울 포인트", "龙魂点"},
	"tracking":          {"tracking", "추적", "追踪"},
	"dead":              {"dead", "사망", "阵亡"},
	"estimated":         {"est.", "추정", "估算"},
	"no_live_minimap":   {"no live minimap (broadcast replay/desk)", "라이브 미니맵 없음 (방송 리플레이/데스크)", "无实时小地图（直播回放/解说台）"},
	"detecting_layout":  {"detecting broadcast layout", "방송 레이아웃 확인 중", "正在识别直播布局"},
	"loading_icons":     {"loading champion icons", "챔피언 아이콘 불러오는 중", "正在加载英雄图标"},
	"buffering":         {"buffering", "버퍼링", "缓冲中"},
	"buffering_video":   {"buffering video", "영상 버퍼링", "视频缓冲"},
	"finding_game":      {"finding the game start in the broadcast", "방송에서 경기 시작 지점을 찾는 중", "正在直播中查找比赛开始位置"},
	"loading_game_data": {"loading game data %d%%", "게임 데이터 %d%% 불러오는 중", "正在加载对局数据 %d%%"},
	"buffering_n":       {"buffering %ds", "%d초 버퍼링", "缓冲 %d 秒"},
	"move":              {"move", "이동", "移动"},
	"open":              {"open", "열기", "打开"},
	"tab":               {"tab", "탭", "切换"},
	"more":              {"more", "더 보기", "更多"},
	"pane":              {"pane", "창", "面板"},
	"pause":             {"pause", "일시정지", "暂停"},
	"seek":              {"seek", "탐색", "跳转"},
	"speed":             {"speed", "배속", "速度"},
	"help":              {"help", "도움말", "帮助"},
	"quit":              {"quit", "종료", "退出"},
	"back":              {"back", "뒤로", "返回"},
	"close":             {"close", "닫기", "关闭"},
	"change":            {"change", "변경", "更改"},
	"zoom":              {"zoom", "확대", "缩放"},
	"view":              {"view", "보기", "视图"},
	"spark":             {"spark", "스파크", "曲线"},
	"obj":               {"obj", "오브젝트", "资源"},
	"map":               {"map", "지도", "地图"},
	"video":             {"video", "영상", "视频"},
	"everywhere":        {"Everywhere", "어디서나", "全局"},
	"lists":             {"Lists", "목록", "列表"},
	"board_help":        {"Board (main pane focused)", "보드 (메인 창 포커스)", "看板（主面板聚焦）"},
	"next_pane":         {"next / previous pane", "다음 / 이전 창", "下一个 / 上一个面板"},
	"focus_panes":       {"focus Leagues / Matches / Games / main pane", "리그 / 경기 / 세트 / 메인 창 포커스", "聚焦联赛 / 比赛 / 对局 / 主面板"},
	"options_help":      {"options (minimap drawing, video sync, poll, left column)", "옵션 (미니맵, 영상 동기화, 폴링, 왼쪽 열)", "选项（小地图、视频同步、轮询、左栏）"},
	"this_help":         {"this help", "이 도움말", "本帮助"},
	"quit_help":         {"quit", "종료", "退出"},
	"esc_help":          {"back: close popup, unzoom, leave the board, close the board", "뒤로: 팝업 닫기, 확대 해제, 보드 닫기", "返回：关闭弹窗、取消缩放、关闭看板"},
	"help_mouse":        {"click: focus and select · double-click: open · wheel: scroll", "클릭: 창 선택·항목 이동 · 더블클릭: 열기 · 휠: 스크롤", "单击：聚焦并选择 · 双击：打开 · 滚轮：滚动"},
	"help_move":         {"move", "이동", "移动"},
	"help_page":         {"page (PgDn at the end loads older / newer matches)", "페이지 (끝에서 PgDn = 더 불러오기)", "翻页（末尾 PgDn 加载更多）"},
	"help_first":        {"first / last", "처음 / 끝", "开头 / 结尾"},
	"help_prev":         {"previous / next pane", "이전 / 다음 창", "上一个 / 下一个面板"},
	"help_tab":          {"Live / Upcoming / Recent tab (Matches)", "라이브 / 예정 / 최근 탭 (경기)", "直播 / 即将开始 / 最近 标签（比赛）"},
	"help_reload":       {"reload the league's matches", "리그 경기 다시 불러오기", "重新加载联赛比赛"},
	"help_open":         {"open: live match → live board, finished → replay", "열기: 라이브 → 라이브 보드, 종료 → 다시 보기", "打开：直播→直播看板，已结束→回放"},
	"help_stats":        {"stats / items / runes", "스탯 / 아이템 / 룬", "数据 / 装备 / 符文"},
	"help_spark":        {"gold sparkline", "골드 차이 스파크라인", "金币差曲线"},
	"help_obj":          {"objective map", "오브젝트 맵", "资源地图"},
	"help_map":          {"minimap panel", "미니맵 패널", "小地图面板"},
	"help_video":        {"broadcast video sync", "방송 영상 동기화", "直播视频同步"},
	"help_zoom":         {"zoom the board", "보드 확대", "放大看板"},
	"help_pause":        {"pause / resume", "일시정지 / 재생", "暂停 / 继续"},
	"help_seek":         {"seek ±10 s", "±10초 이동", "跳转 ±10 秒"},
	"help_speed":        {"speed 1× 2× 4×", "배속 1× 2× 4×", "速度 1× 2× 4×"},
	"usage": {
		`esterm — esports terminal client

Usage:
  esterm                       interactive app (leagues, matches, live boards, replays)
  esterm <game> <command> [flags]

Games:
  lol    League of Legends (https://lolesports.com endpoints)

  -lang en|ko|zh               UI language (also ESTERM_LANG)

Run 'esterm <game> help' for game commands.`,
		`esterm — e스포츠 터미널 클라이언트

사용법:
  esterm                       대화형 앱 (리그, 경기, 라이브 보드, 다시 보기)
  esterm <game> <command> [flags]

게임:
  lol    League of Legends (https://lolesports.com 엔드포인트)

  -lang en|ko|zh               UI 언어 (ESTERM_LANG 환경변수도 가능)

게임별 명령은 'esterm <game> help'를 실행하세요.`,
		`esterm — 电竞赛事终端客户端

用法:
  esterm                       交互式应用（联赛、比赛、直播看板、回放）
  esterm <game> <command> [flags]

游戏:
  lol    League of Legends (https://lolesports.com 接口)

  -lang en|ko|zh               UI 语言（也可用 ESTERM_LANG）

运行 'esterm <game> help' 查看游戏命令。`,
	},
}
