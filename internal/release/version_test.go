package release

import (
	"errors"
	"testing"
)

func TestParse(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want Version
		ok   bool
	}{
		{"1.2.3", Version{1, 2, 3}, true},
		{"v1.2.3", Version{1, 2, 3}, true},
		{"V10.0.1", Version{10, 0, 1}, true},
		{"  2.0.0 ", Version{2, 0, 0}, true},
		{"1.2", Version{}, false},
		{"1.2.3.4", Version{}, false},
		{"1.2.x", Version{}, false},
		{"1.-2.3", Version{}, false},
		{"1.2.3-rc1", Version{}, false},
		{"", Version{}, false},
	}
	for _, tc := range cases {
		got, err := Parse(tc.in)
		if tc.ok != (err == nil) {
			t.Errorf("Parse(%q) err=%v", tc.in, err)
			continue
		}
		if tc.ok && got != tc.want {
			t.Errorf("Parse(%q)=%v", tc.in, got)
		}
	}
}

func TestCompare(t *testing.T) {
	t.Parallel()

	cases := []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.3", 0},
		{"1.2.3", "1.2.4", -1},
		{"1.2.4", "1.2.3", 1},
		{"2.0.0", "1.99.99", 1},
		{"1.10.0", "1.9.0", 1},
		{"v1.0.0", "1.0.0", 0},
	}
	for _, tc := range cases {
		got, err := Compare(tc.a, tc.b)
		if err != nil || got != tc.want {
			t.Errorf("Compare(%s,%s)=%d,%v 期望 %d", tc.a, tc.b, got, err, tc.want)
		}
	}
}

func TestDecideUpgrade(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		current        string
		target         string
		allowDowngrade bool
		wantErr        error
	}{
		{"升级", "1.2.3", "1.3.0", false, nil},
		{"相同版本", "1.2.3", "1.2.3", false, ErrSameVersion},
		{"默认拒绝降级", "2.0.0", "1.9.0", false, ErrDowngradeDenied},
		{"显式允许降级", "2.0.0", "1.9.0", true, nil},
		{"允许降级对升级无影响", "1.0.0", "2.0.0", true, nil},
		{"目标版本非法", "1.0.0", "nope", false, ErrInvalidVersion},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := DecideUpgrade(tc.current, tc.target, tc.allowDowngrade)
			if tc.wantErr == nil {
				if err != nil {
					t.Errorf("期望通过，实际 %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("期望 %v，实际 %v", tc.wantErr, err)
			}
		})
	}
}
