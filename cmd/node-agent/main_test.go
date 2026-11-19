package main

import "testing"

func TestProfileSlug(t *testing.T) {
	for path, want := range map[string]string{
		"/etc/fake-gpu/profiles/mi300x.yaml": "mi300x",
		"profiles/mi250x.yaml":               "mi250x",
		"/etc/fake-gpu/config.yaml":          "mi300x",
		"captured.yml":                       "captured",
		"mi210":                              "mi210",
	} {
		if got := profileSlug(path); got != want {
			t.Errorf("profileSlug(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestMakedev(t *testing.T) {
	for _, tc := range []struct {
		major, minor uint32
		want         uint64
	}{
		{234, 0, 234 << 8},
		{226, 128, 226<<8 | 128},
		{226, 7, 226<<8 | 7},
	} {
		if got := makedev(tc.major, tc.minor); got != tc.want {
			t.Errorf("makedev(%d, %d) = %#x, want %#x", tc.major, tc.minor, got, tc.want)
		}
	}
}
