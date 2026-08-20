package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// 天气代理（供 App 首页天气头用）
//
// 为什么走服务端代理而不是 App 直连：
//  1. AppCode 是凭据，放服务端不随 APK 分发（APK 可被反编译提取）。
//  2. 这个 ip-to-weather 接口按**调用方传入的 IP** 定位；服务端天然知道客户端 IP，
//     直连的话 App 还得先查自己的公网 IP，多一次外部请求。
//  3. 上游返回的**中文城市/天气是双重编码乱码**（GBK 当 UTF-16），必须服务端清洗：
//     只取干净的数字字段（温度、weather_code、湿度、AQI），中文由我们按 code 映射。
//
// 城市定位优先级（自动识别用户所在城市，不再写死杭州）：
//  1. ?ip= 显式覆盖（调试用；Android App 会传入手机自身公网 IP，避免落到服务器出口城市）。
//  2. 客户端公网 IP：用户走公网访问时，clientIP 即其真实公网 IP → 其所在城市。
//  3. 客户端是内网/环回 IP（本产品服务端与用户同处一个局域网/园区最常见）：
//     自动探测**服务端出口公网 IP**——出口 IP 即园区所在城市的公网 IP，
//     等价于用户所在城市。结果缓存，出口 IP 基本不变。
//  4. 出口探测失败时才回退到 AIOPS_WEATHER_DEFAULT_IP（如配置，纯兜底/强制指定城市）。
//
// 城市中文名：优先取上游合法汉字字段；否则用 c2 拼音经 weatherCityCN 大表映射。
// AppCode 从环境变量 AIOPS_WEATHER_APPCODE 读取；未配置则天气功能关闭（App 优雅降级）。
// AIOPS_WEATHER_DEFAULT_IP 可选：仅当出口探测失败时兜底，或云上部署强制指定城市时使用。
// ---------------------------------------------------------------------------

const weatherUpstream = "https://weather01.market.alicloudapi.com/ip-to-weather"
const weatherCacheTTL = 20 * time.Minute

type weatherResult struct {
	OK           bool   `json:"ok"`
	Location     string `json:"location"`           // 展示用：优先「市 · 区县」，与上游 IP 定位粒度一致
	City         string `json:"city,omitempty"`     // 地级市 / 直辖市
	District     string `json:"district,omitempty"` // 区 / 县（IP 落到区县时有值）
	TempC        int    `json:"temp_c"`
	Text         string `json:"text"`
	Humidity     string `json:"humidity,omitempty"`
	AQI          string `json:"aqi,omitempty"`
	TodayHigh    int    `json:"today_high,omitempty"`
	TodayLow     int    `json:"today_low,omitempty"`
	TomorrowHigh int    `json:"tomorrow_high,omitempty"`
	TomorrowLow  int    `json:"tomorrow_low,omitempty"`
	TomorrowText string `json:"tomorrow_text,omitempty"` // 明天白天天气（按 code 映射，避免上游中文乱码）
	Reason       string `json:"reason,omitempty"`        // ok=false 时说明原因
	Source       string `json:"source,omitempty"`        // 定位 IP 来源：client/egress/default-env/override（调试用）
}

type weatherCacheEntry struct {
	res weatherResult
	at  time.Time
}

var (
	weatherMu    sync.Mutex
	weatherCache = map[string]weatherCacheEntry{}
	weatherHTTP  = &http.Client{Timeout: 8 * time.Second}
)

// handleWeather returns normalized current weather for the caller's location.
func (s *Server) handleWeather(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.currentUser(r); !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": Tr(r, "auth.unauthorized")})
		return
	}
	appCode := strings.TrimSpace(os.Getenv("AIOPS_WEATHER_APPCODE"))
	if appCode == "" {
		writeJSON(w, http.StatusOK, weatherResult{OK: false, Reason: "weather not configured"})
		return
	}

	ip := r.URL.Query().Get("ip") // 允许显式覆盖（调试用）
	source := "override"
	if ip == "" {
		ip = s.clientIP(r)
		source = "client"
	}
	// 客户端是内网/环回 IP（服务端与用户同处局域网时最常见）：无法用它定位。
	// 改为自动探测**服务端出口公网 IP**（园区出口=用户所在城市）；探测失败才回退默认 IP。
	if isPrivateOrLoopback(ip) {
		if eip := serverEgressIP(); eip != "" {
			ip = eip
			source = "egress"
		} else if def := strings.TrimSpace(os.Getenv("AIOPS_WEATHER_DEFAULT_IP")); def != "" {
			ip = def
			source = "default-env"
		} else {
			writeJSON(w, http.StatusOK, weatherResult{OK: false, Reason: "client ip not routable; egress detect failed"})
			return
		}
	}

	// 缓存：天气不必秒级刷新，同一 IP 20 分钟内复用，省调用配额。
	weatherMu.Lock()
	if e, ok := weatherCache[ip]; ok && time.Since(e.at) < weatherCacheTTL {
		weatherMu.Unlock()
		res := e.res
		res.Source = source
		writeJSON(w, http.StatusOK, res)
		return
	}
	weatherMu.Unlock()

	res, err := fetchWeather(appCode, ip)
	if err != nil {
		writeJSON(w, http.StatusOK, weatherResult{OK: false, Reason: err.Error()})
		return
	}
	weatherMu.Lock()
	weatherCache[ip] = weatherCacheEntry{res: res, at: time.Now()}
	weatherMu.Unlock()
	res.Source = source
	writeJSON(w, http.StatusOK, res)
}

// ---------------------------------------------------------------------------
// 服务端出口公网 IP 探测
//
// 本产品多为内网/园区部署，服务端与用户同处一个局域网，出口公网 IP 即用户所在
// 城市的公网 IP。依次尝试几个**国内可达**的 IP 回显服务，取第一个成功返回的公网
// IPv4。结果缓存 6 小时（出口 IP 基本不变）；失败短暂缓存 1 分钟，避免离线时反复打。
// ---------------------------------------------------------------------------

const (
	egressTTL     = 6 * time.Hour
	egressFailTTL = 1 * time.Minute
)

var (
	egressMu   sync.Mutex
	egressIP   string
	egressAt   time.Time
	egressOK   bool
	egressHTTP = &http.Client{Timeout: 4 * time.Second}
	ipv4Re     = regexp.MustCompile(`(?:\d{1,3}\.){3}\d{1,3}`)

	// 国内可达、返回体里含**调用方公网 IP** 的回显服务，任一成功即可。全部为国内节点：
	// 国际线路（如 ipify）在国内常经不同出口，会解析出另一个 IP → 错误城市，故不用。
	egressProviders = []string{
		"https://myip.ipip.net",         // 文本：当前 IP：1.2.3.4 来自于：中国 上海 ...
		"https://ip.3322.net",           // 纯文本 IP：1.2.3.4
		"https://ddns.oray.com/checkip", // 文本：Current IP Address: 1.2.3.4
		"https://cip.cc",                // 文本：IP : 1.2.3.4  地址 : 中国 ...
	}
)

// serverEgressIP returns the server's public egress IPv4, cached. Empty on failure.
func serverEgressIP() string {
	egressMu.Lock()
	if egressIP != "" && time.Since(egressAt) < egressTTL {
		ip := egressIP
		egressMu.Unlock()
		return ip
	}
	if !egressOK && egressIP == "" && time.Since(egressAt) < egressFailTTL {
		egressMu.Unlock() // 近期刚失败，先别重试
		return ""
	}
	egressMu.Unlock()

	found := probeEgressIP()

	egressMu.Lock()
	egressAt = time.Now()
	if found != "" {
		egressIP = found
		egressOK = true
	} else {
		egressOK = false
	}
	egressMu.Unlock()
	return found
}

func probeEgressIP() string {
	for _, url := range egressProviders {
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "curl/8.0") // 部分回显服务对无 UA 返回 HTML
		resp, err := egressHTTP.Do(req)
		if err != nil {
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		if resp.StatusCode != 200 {
			continue
		}
		// 取返回体里第一个**公网** IPv4（跳过内网/环回，防回显服务把内网地址塞进来）。
		for _, cand := range ipv4Re.FindAllString(string(raw), -1) {
			if net.ParseIP(cand) != nil && !isPrivateOrLoopback(cand) {
				return cand
			}
		}
	}
	return ""
}

func fetchWeather(appCode, ip string) (weatherResult, error) {
	req, _ := http.NewRequest("GET", weatherUpstream+"?ip="+ip, nil)
	req.Header.Set("Authorization", "APPCODE "+appCode)
	resp, err := weatherHTTP.Do(req)
	if err != nil {
		return weatherResult{}, fmt.Errorf("weather upstream: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		return weatherResult{}, fmt.Errorf("weather upstream HTTP %d", resp.StatusCode)
	}

	// ShowAPI cityInfo：
	//   c2/c3 = 本次定位点（常为区县）拼音/中文
	//   c4/c5 = 省拼音/中文
	//   c6/c7 = 所属地级市拼音/中文（区县定位时有值）
	// 中文偶发乱码；有合法汉字则用，否则走拼音映射表。
	var up struct {
		Code int `json:"showapi_res_code"`
		Body struct {
			CityInfo struct {
				C2 string `json:"c2"`
				C3 string `json:"c3"`
				C4 string `json:"c4"`
				C5 string `json:"c5"`
				C6 string `json:"c6"`
				C7 string `json:"c7"`
			} `json:"cityInfo"`
			Now struct {
				Temperature string `json:"temperature"`
				WeatherCode string `json:"weather_code"`
				SD          string `json:"sd"`
				AQI         string `json:"aqi"`
			} `json:"now"`
			F1 struct {
				DayTemp   string `json:"day_air_temperature"`
				NightTemp string `json:"night_air_temperature"`
			} `json:"f1"`
			F2 struct {
				DayTemp   string `json:"day_air_temperature"`
				NightTemp string `json:"night_air_temperature"`
				DayCode   string `json:"day_weather_code"`
			} `json:"f2"`
		} `json:"showapi_res_body"`
	}
	if err := json.Unmarshal(raw, &up); err != nil {
		return weatherResult{}, fmt.Errorf("weather parse: %w", err)
	}
	if up.Code != 0 {
		return weatherResult{}, fmt.Errorf("weather upstream code %d", up.Code)
	}

	city, district, location := resolveWeatherPlace(up.Body.CityInfo.C2, up.Body.CityInfo.C3, up.Body.CityInfo.C4, up.Body.CityInfo.C5, up.Body.CityInfo.C6, up.Body.CityInfo.C7)
	res := weatherResult{
		OK:           true,
		TempC:        weatherAtoi(up.Body.Now.Temperature),
		Text:         weatherCodeText(up.Body.Now.WeatherCode),
		Humidity:     up.Body.Now.SD,
		AQI:          up.Body.Now.AQI,
		Location:     location,
		City:         city,
		District:     district,
		TodayHigh:    weatherAtoi(up.Body.F1.DayTemp),
		TodayLow:     weatherAtoi(up.Body.F1.NightTemp),
		TomorrowHigh: weatherAtoi(up.Body.F2.DayTemp),
		TomorrowLow:  weatherAtoi(up.Body.F2.NightTemp),
		TomorrowText: weatherCodeText(up.Body.F2.DayCode),
	}
	return res, nil
}

func weatherAtoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func isPrivateOrLoopback(ipStr string) bool {
	ip := net.ParseIP(strings.TrimSpace(ipStr))
	if ip == nil {
		return true
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
}

// weatherCodeText maps ShowAPI weather codes to Chinese. Covers the common set;
// unknown codes fall back to a neutral label rather than the upstream mojibake.
func weatherCodeText(code string) string {
	switch strings.TrimSpace(code) {
	case "00":
		return "晴"
	case "01":
		return "多云"
	case "02":
		return "阴"
	case "03":
		return "阵雨"
	case "04":
		return "雷阵雨"
	case "05":
		return "雷阵雨伴冰雹"
	case "06":
		return "雨夹雪"
	case "07":
		return "小雨"
	case "08":
		return "中雨"
	case "09":
		return "大雨"
	case "10":
		return "暴雨"
	case "11":
		return "大暴雨"
	case "12":
		return "特大暴雨"
	case "13":
		return "阵雪"
	case "14":
		return "小雪"
	case "15":
		return "中雪"
	case "16":
		return "大雪"
	case "17":
		return "暴雪"
	case "18":
		return "雾"
	case "19":
		return "冻雨"
	case "20":
		return "沙尘暴"
	case "29":
		return "浮尘"
	case "30":
		return "扬沙"
	case "32":
		return "浓雾"
	case "49", "53", "54", "55", "56", "57":
		return "霾"
	default:
		return "未知"
	}
}

// resolveWeatherPlace builds city / district / display location from ShowAPI
// cityInfo, matching upstream IP granularity (市 + 区县 when both exist).
//
//	c2/c3 — point of IP geo (city or district)
//	c4/c5 — province
//	c6/c7 — parent prefecture city when c2/c3 is a district
func resolveWeatherPlace(c2, c3, c4, c5, c6, c7 string) (city, district, location string) {
	point := pickChineseOrPinyin(c3, c2)
	parent := pickChineseOrPinyin(c7, c6)
	province := pickChineseOrPinyin(c5, c4)

	point = normalizeChinesePlace(point, true)
	parent = normalizeChinesePlace(parent, false)
	province = normalizeChinesePlace(province, false)

	switch {
	case parent != "" && point != "" && parent != point:
		// 区县定位：市 · 区县
		city, district = parent, point
		location = city + " · " + district
	case point != "" && province != "" && province != point && isMunicipality(province):
		// 直辖市下的区：上海 · 奉贤
		city, district = province, point
		location = city + " · " + district
	case point != "" && parent == "" && province != "" && province != point && !isMunicipality(province):
		// 仅有省 + 市（无区）：仍显示市级点
		city = point
		location = city
	case point != "":
		city = point
		location = city
	case parent != "":
		city = parent
		location = city
	case province != "":
		city = province
		location = city
	}
	return city, district, location
}

func pickChineseOrPinyin(chinese, pinyin string) string {
	if loc := normalizeChinesePlace(chinese, true); loc != "" {
		return loc
	}
	return cityCN(pinyin)
}

func isMunicipality(name string) bool {
	switch name {
	case "北京", "上海", "天津", "重庆":
		return true
	default:
		return false
	}
}

func containsHan(s string) bool {
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}

// normalizeChinesePlace keeps Han strings; keepDistrictSuffix controls whether
// 「区/县/旗」are preserved (区县展示) or stripped (市级名「杭州市」→「杭州」).
func normalizeChinesePlace(s string, keepDistrictSuffix bool) string {
	s = strings.TrimSpace(s)
	if s == "" || !containsHan(s) {
		return ""
	}
	latin := 0
	for _, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			latin++
		}
	}
	if latin > 2 {
		return ""
	}
	runes := []rune(s)
	if keepDistrictSuffix {
		// 保留「浦东新区」；「奉贤区」「余杭区」去掉「区」更利展示
		if strings.HasSuffix(s, "新区") || strings.HasSuffix(s, "矿区") || strings.HasSuffix(s, "自治区") {
			return s
		}
		for _, suf := range []string{"市", "地区", "盟"} {
			if strings.HasSuffix(s, suf) && len(runes) > len([]rune(suf))+1 {
				return strings.TrimSuffix(s, suf)
			}
		}
		for _, suf := range []string{"区", "县", "旗"} {
			if strings.HasSuffix(s, suf) && len(runes) <= 4 && len(runes) > len([]rune(suf))+1 {
				return strings.TrimSuffix(s, suf)
			}
		}
		return s
	}
	for _, suf := range []string{"市", "地区", "盟", "壮族自治区", "回族自治区", "维吾尔自治区", "自治区"} {
		if strings.HasSuffix(s, suf) && len(runes) > len([]rune(suf))+1 {
			return strings.TrimSuffix(s, suf)
		}
	}
	return s
}

// resolveWeatherCity is kept for older call sites / tests — single-label fallback.
func resolveWeatherCity(pinyin string, chineseCandidates ...string) string {
	for _, c := range chineseCandidates {
		if loc := normalizeChinesePlace(c, true); loc != "" {
			return loc
		}
	}
	return cityCN(pinyin)
}

// normalizeChineseCity wraps normalizeChinesePlace for city-level stripping.
func normalizeChineseCity(s string) string {
	return normalizeChinesePlace(s, false)
}

// cityCN maps city pinyin (ShowAPI c2) to Chinese. Falls back to Title-cased
// pinyin only when unknown — expand this table rather than showing Latin names
// to end users. Keys are lowercase, no spaces; optional trailing "shi" is stripped.
func cityCN(pinyin string) string {
	p := strings.ToLower(strings.TrimSpace(pinyin))
	p = strings.ReplaceAll(p, " ", "")
	p = strings.ReplaceAll(p, "-", "")
	p = strings.TrimSuffix(p, "shi")
	if p == "" {
		return ""
	}
	if cn, ok := weatherCityCN[p]; ok {
		return cn
	}
	// District / county often arrives as "xxxqu" / "xxx县" pinyin variants.
	for _, suf := range []string{"qu", "xian", "zhou", "meng"} {
		if strings.HasSuffix(p, suf) && len(p) > len(suf)+2 {
			if cn, ok := weatherCityCN[strings.TrimSuffix(p, suf)]; ok {
				return cn
			}
		}
	}
	return strings.ToUpper(p[:1]) + p[1:]
}

// weatherCityCN: major PRC cities + common districts seen in IP geo (non-exhaustive).
var weatherCityCN = map[string]string{
	// 直辖市
	"beijing": "北京", "shanghai": "上海", "tianjin": "天津", "chongqing": "重庆",
	// 省会 / 副省级 / 热门地级市
	"guangzhou": "广州", "shenzhen": "深圳", "dongguan": "东莞", "foshan": "佛山",
	"zhuhai": "珠海", "huizhou": "惠州", "zhongshan": "中山", "jiangmen": "江门",
	"zhanjiang": "湛江", "maoming": "茂名", "zhaoqing": "肇庆", "shantou": "汕头",
	"hangzhou": "杭州", "ningbo": "宁波", "wenzhou": "温州", "jiaxing": "嘉兴",
	"huzhou": "湖州", "shaoxing": "绍兴", "jinhua": "金华", "quzhou": "衢州",
	"zhoushan": "舟山", "taizhou": "台州", "lishui": "丽水", "yiwu": "义乌",
	"nanjing": "南京", "wuxi": "无锡", "xuzhou": "徐州", "changzhou": "常州",
	"suzhou": "苏州", "nantong": "南通", "lianyungang": "连云港", "huaian": "淮安",
	"yancheng": "盐城", "yangzhou": "扬州", "zhenjiang": "镇江", "suqian": "宿迁",
	"hefei": "合肥", "wuhu": "芜湖", "bengbu": "蚌埠", "huainan": "淮南",
	"maanshan": "马鞍山", "huaibei": "淮北", "tongling": "铜陵", "anqing": "安庆",
	"huangshan": "黄山", "chuzhou": "滁州", "fuyang": "阜阳", "suzhouanhui": "宿州",
	"luan": "六安", "bozhou": "亳州", "chizhou": "池州", "xuancheng": "宣城",
	"fuzhou": "福州", "xiamen": "厦门", "putian": "莆田", "sanming": "三明",
	"quanzhou": "泉州", "zhangzhou": "漳州", "nanping": "南平", "longyan": "龙岩",
	"ningde":   "宁德",
	"nanchang": "南昌", "jingdezhen": "景德镇", "pingxiang": "萍乡", "jiujiang": "九江",
	"xinyu": "新余", "yingtan": "鹰潭", "ganzhou": "赣州", "jian": "吉安",
	"yichun": "宜春", "fuzhoujx": "抚州", "shangrao": "上饶",
	"jinan": "济南", "qingdao": "青岛", "zibo": "淄博", "zaozhuang": "枣庄",
	"dongying": "东营", "yantai": "烟台", "weifang": "潍坊", "jining": "济宁",
	"taian": "泰安", "weihai": "威海", "rizhao": "日照", "linyi": "临沂",
	"dezhou": "德州", "liaocheng": "聊城", "binzhou": "滨州", "heze": "菏泽",
	"zhengzhou": "郑州", "kaifeng": "开封", "luoyang": "洛阳", "pingdingshan": "平顶山",
	"anyang": "安阳", "hebi": "鹤壁", "xinxiang": "新乡", "jiaozuo": "焦作",
	"puyang": "濮阳", "xuchang": "许昌", "luohe": "漯河", "sanmenxia": "三门峡",
	"nanyang": "南阳", "shangqiu": "商丘", "xinyang": "信阳", "zhoukou": "周口",
	"zhumadian": "驻马店",
	"wuhan":     "武汉", "huangshi": "黄石", "shiyan": "十堰", "yichang": "宜昌",
	"xiangyang": "襄阳", "ezhou": "鄂州", "jingmen": "荆门", "xiaogan": "孝感",
	"jingzhou": "荆州", "huanggang": "黄冈", "xianning": "咸宁", "suizhou": "随州",
	"changsha": "长沙", "zhuzhou": "株洲", "xiangtan": "湘潭", "hengyang": "衡阳",
	"shaoyang": "邵阳", "yueyang": "岳阳", "changde": "常德", "zhangjiajie": "张家界",
	"yiyang": "益阳", "chenzhou": "郴州", "yongzhou": "永州", "huaihua": "怀化",
	"loudi":     "娄底",
	"guangyuan": "广元", "chengdu": "成都", "mianyang": "绵阳", "deyang": "德阳",
	"nanchong": "南充", "yibin": "宜宾", "luzhou": "泸州", "leshan": "乐山",
	"meishan": "眉山", "zigong": "自贡", "panzhihua": "攀枝花", "suining": "遂宁",
	"neijiang": "内江", "guangan": "广安", "dazhou": "达州", "yaan": "雅安",
	"bazhong": "巴中", "ziyang": "资阳",
	"guiyang": "贵阳", "zunyi": "遵义", "liupanshui": "六盘水", "anshun": "安顺",
	"kunming": "昆明", "qujing": "曲靖", "yuxi": "玉溪", "baoshan": "保山",
	"zhaotong": "昭通", "lijiang": "丽江", "puer": "普洱", "lincang": "临沧",
	"xian": "西安", "xianyang": "咸阳", "baoji": "宝鸡", "weinan": "渭南",
	"tongchuan": "铜川", "yanan": "延安", "hanzhong": "汉中", "yulin": "榆林",
	"ankang": "安康", "shangluo": "商洛",
	"lanzhou": "兰州", "jiayuguan": "嘉峪关", "jinchang": "金昌", "baiyin": "白银",
	"tianshui": "天水", "wuwei": "武威", "zhangye": "张掖", "pingliang": "平凉",
	"jiuquan": "酒泉", "qingyang": "庆阳", "dingxi": "定西", "longnan": "陇南",
	"xining": "西宁", "haikou": "海口", "sanya": "三亚", "danzhou": "儋州",
	"nanning": "南宁", "liuzhou": "柳州", "guilin": "桂林", "wuzhou": "梧州",
	"beihai": "北海", "fangchenggang": "防城港", "qinzhou": "钦州", "guigang": "贵港",
	"yulin_gx": "玉林", "baise": "百色", "hezhou": "贺州", "hechi": "河池", "laibin": "来宾", "chongzuo": "崇左",
	"harbin": "哈尔滨", "qiqihaer": "齐齐哈尔", "jixi": "鸡西", "hegang": "鹤岗",
	"shuangyashan": "双鸭山", "daqing": "大庆", "yichunhlj": "伊春", "jiamusi": "佳木斯",
	"qitaihe": "七台河", "mudanjiang": "牡丹江", "heihe": "黑河", "suihua": "绥化",
	"changchun": "长春", "jilin": "吉林", "siping": "四平", "liaoyuan": "辽源",
	"tonghua": "通化", "baishan": "白山", "songyuan": "松原", "baicheng": "白城",
	"shenyang": "沈阳", "dalian": "大连", "anshan": "鞍山", "fushun": "抚顺",
	"benxi": "本溪", "dandong": "丹东", "jinzhou": "锦州", "yingkou": "营口",
	"fuxin": "阜新", "liaoyang": "辽阳", "panjin": "盘锦", "tieling": "铁岭",
	"chaoyang": "朝阳", "huludao": "葫芦岛",
	"hohhot": "呼和浩特", "huhehaote": "呼和浩特", "baotou": "包头", "wuhai": "乌海",
	"chifeng": "赤峰", "tongliao": "通辽", "ordos": "鄂尔多斯", "eerduosi": "鄂尔多斯",
	"hulunbuir": "呼伦贝尔", "bayannur": "巴彦淖尔", "ulanqab": "乌兰察布",
	"yinchuan": "银川", "shizuishan": "石嘴山", "wuzhong": "吴忠", "guyuan": "固原", "zhongwei": "中卫",
	"urumqi": "乌鲁木齐", "wulumuqi": "乌鲁木齐", "karamay": "克拉玛依", "kelamayi": "克拉玛依",
	"turpan": "吐鲁番", "hami": "哈密",
	"lhasa": "拉萨", "lasa": "拉萨",
	"taipei": "台北", "taichung": "台中", "kaohsiung": "高雄", "gaoxiong": "高雄",
	"hongkong": "香港", "xianggang": "香港", "macau": "澳门", "aomen": "澳门",
	// 常见区县（IP 库常落到区级）
	"fengxian": "奉贤", "pudong": "浦东", "minhang": "闵行", "baoshan_sh": "宝山",
	"jiading": "嘉定", "songjiang": "松江", "qingpu": "青浦", "jinshan": "金山",
	"chongming": "崇明", "xuhui": "徐汇", "changning": "长宁", "jingan": "静安",
	"putuo": "普陀", "hongkou": "虹口", "yangpu": "杨浦", "huangpu": "黄埔",
	"chaoyang_bj": "朝阳", "haidian": "海淀", "fengtai": "丰台", "shijingshan": "石景山",
	"tongzhou": "通州", "changping": "昌平", "daxing": "大兴", "shunyi": "顺义",
	"huairou": "怀柔", "pinggu": "平谷", "miyun": "密云", "yanqing": "延庆",
	"nanshan": "南山", "futian": "福田", "luohu": "罗湖", "baoan": "宝安",
	"longgang": "龙岗", "yantian": "盐田", "longhua": "龙华", "pingshan": "坪山",
	"yuhang": "余杭", "xiaoshan": "萧山", "binjiang": "滨江", "linping": "临平",
	"qiantang": "钱塘", "fuyanghangzhou": "富阳", "linan": "临安",
	"jiangning": "江宁", "pukou": "浦口", "qixia": "栖霞", "yuhuatai": "雨花台",
	"wuhou": "武侯", "jinjiang_cd": "锦江", "qingyang_cd": "青羊", "jinniu": "金牛",
	"chenghua": "成华", "pidu": "郫都", "xindu": "新都", "wenjiang": "温江",
	"shuangliu": "双流", "longquanyi": "龙泉驿",
	"jianghan": "江汉", "wuchang": "武昌", "hankou": "汉口", "hanyang": "汉阳",
	"hongshan": "洪山", "dongxihu": "东西湖", "jiangxia": "江夏", "huangpi": "黄陂",
	"xinbei": "新北", "tianhe": "天河", "yuexiu": "越秀", "haizhu": "海珠",
	"liwan": "荔湾", "baiyun": "白云", "huangpugz": "黄埔", "panyu": "番禺",
	"huadu": "花都", "nansha": "南沙", "conghua": "从化", "zengcheng": "增城",
	"yubei": "渝北", "yuzhong": "渝中", "jiangbei": "江北", "nanan": "南岸",
	"shapingba": "沙坪坝", "jiulongpo": "九龙坡", "dadukou": "大渡口", "banan": "巴南",
	"beibei": "北碚",
}
