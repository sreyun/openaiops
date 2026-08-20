package main

import "testing"

func TestCityCNCommonCities(t *testing.T) {
	cases := map[string]string{
		"hangzhou":    "杭州",
		"HangZhou":    "杭州",
		"hangzhoushi": "杭州",
		"wuxi":        "无锡",
		"fengxian":    "奉贤",
		"beijing":     "北京",
		"shanghai":    "上海",
		"shenzhen":    "深圳",
		"chengdu":     "成都",
	}
	for in, want := range cases {
		if got := cityCN(in); got != want {
			t.Errorf("cityCN(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveWeatherCityPrefersChinese(t *testing.T) {
	got := resolveWeatherCity("wuxi", "无锡市", "")
	if got != "无锡" {
		t.Errorf("prefer Chinese: got %q", got)
	}
	got = resolveWeatherCity("wuxi", "mojibakeXYZ", "")
	if got != "无锡" {
		t.Errorf("fallback pinyin map: got %q", got)
	}
}

func TestResolveWeatherPlaceCityDistrict(t *testing.T) {
	// 区县定位：c3=区，c7=市
	city, district, loc := resolveWeatherPlace("fengxian", "奉贤区", "shanghai", "上海", "shanghai", "上海市")
	if city != "上海" || district != "奉贤" || loc != "上海 · 奉贤" {
		t.Errorf("district geo: city=%q district=%q loc=%q", city, district, loc)
	}
	// 直辖市区：仅有省 + 区
	city, district, loc = resolveWeatherPlace("pudong", "浦东新区", "shanghai", "上海", "", "")
	if city != "上海" || district != "浦东新区" || loc != "上海 · 浦东新区" {
		t.Errorf("municipality district: city=%q district=%q loc=%q", city, district, loc)
	}
	// 纯地级市
	city, district, loc = resolveWeatherPlace("hangzhou", "杭州市", "zhejiang", "浙江", "", "")
	if city != "杭州" || district != "" || loc != "杭州" {
		t.Errorf("city only: city=%q district=%q loc=%q", city, district, loc)
	}
}

func TestNormalizeChineseCity(t *testing.T) {
	if got := normalizeChineseCity("杭州市"); got != "杭州" {
		t.Errorf("strip 市: got %q", got)
	}
	if got := normalizeChineseCity("Wuxi"); got != "" {
		t.Errorf("reject latin: got %q", got)
	}
}

func TestIsPrivateOrLoopback(t *testing.T) {
	if !isPrivateOrLoopback("10.0.0.1") || !isPrivateOrLoopback("192.168.1.1") {
		t.Error("private IPs should be private")
	}
	if isPrivateOrLoopback("1.2.3.4") {
		t.Error("public IP should not be private")
	}
}
