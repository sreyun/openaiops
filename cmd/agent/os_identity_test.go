package main

import "testing"

func TestNormalizeDistroMajor(t *testing.T) {
	cases := map[string]string{
		"9.4":                             "9",
		"10.0":                            "10",
		"22.03":                           "22",
		"24.03":                           "24",
		"2.0":                             "2",
		"2.5":                             "2",
		"3":                               "3",
		"V10":                             "10",
		"v11":                             "11",
		"V10 (Sword)":                     "10",
		"Kylin Linux Advanced Server V10": "10",
		"Kylin Linux Advanced Server V11": "11",
		"Rocky Linux 9.3 (Blue Onyx)":     "9",
		"Rocky Linux 10.0 (Red Quartz)":   "10",
		"openEuler 22.03 LTS":             "22",
		"openEuler 24.03 LTS":             "24",
		"EulerOS 2.0 (SP10)":              "2",
		"Alibaba Cloud Linux 3.2104":      "3",
		"Debian GNU/Linux 12 (bookworm)":  "12",
		// Must not steal major via a stray "v10" token on non-Kylin text.
		"openEuler 22.03 with libv10": "22",
		"":                            "",
	}
	for in, want := range cases {
		if got := normalizeDistroMajor(in); got != want {
			t.Errorf("normalizeDistroMajor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClassifyLinuxFamily(t *testing.T) {
	cases := []struct {
		blob, id, want string
	}{
		{"rocky rhel centos fedora", "rocky", "rhel"},
		{"kylin linux advanced server", "kylin", "kylin"},
		{"neokylin", "neokylin", "kylin"},
		{"ubuntu debian", "ubuntu", "debian"},
		{"uos deepin", "uos", "uos"},
		{"openeuler", "openeuler", "rhel"},
		{"euleros", "euleros", "rhel"},
		{"alibaba cloud linux alinux", "alinux", "rhel"},
		{"centos", "centos", "rhel"},
	}
	for _, c := range cases {
		if got := classifyLinuxFamily(c.blob, c.id); got != c.want {
			t.Errorf("classifyLinuxFamily(%q,%q) = %q, want %q", c.blob, c.id, got, c.want)
		}
	}
}

func TestClassifyLinuxPkgKylin(t *testing.T) {
	if got := classifyLinuxPkg("kylin debian ubuntu", "kylin"); got != "deb" {
		t.Errorf("kylin desktop pkg = %q, want deb", got)
	}
	if got := classifyLinuxPkg("kylin rhel centos fedora", "kylin"); got != "rpm" {
		t.Errorf("kylin server pkg = %q, want rpm", got)
	}
	if got := classifyLinuxPkg("rocky rhel", "rhel"); got != "rpm" {
		t.Errorf("rocky pkg = %q, want rpm", got)
	}
}
