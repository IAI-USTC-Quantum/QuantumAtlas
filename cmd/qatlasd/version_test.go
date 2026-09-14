package main

import "testing"

func TestResolveVersion(t *testing.T) {
	for _, tc := range []struct{ injected, module, want string }{
		{"0.35.0", "v0.34.0", "0.35.0"},
		{"v0.35.0-rc.1+build.2", "v0.34.0", "0.35.0-rc.1+build.2"},
		{"dev", "v0.35.0", "0.35.0"},
		{"", "v0.35.0-rc.1", "0.35.0-rc.1"},
		{"dev", "v0.0.0-20260401000000-abcdefabcdef", "0.0.0-20260401000000-abcdefabcdef"},
		{"dev", "(devel)", "dev"},
		{"", "", "dev"},
		{"0.35.0-SNAPSHOT-abcd", "(devel)", "0.35.0-SNAPSHOT-abcd"},
	} {
		if got := resolveVersion(tc.injected, tc.module); got != tc.want {
			t.Errorf("resolveVersion(%q, %q) = %q, want %q", tc.injected, tc.module, got, tc.want)
		}
	}
}
