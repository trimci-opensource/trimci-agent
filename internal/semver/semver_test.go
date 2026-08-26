package semver

import "testing"

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.2", "1.2.0", 0},
		{"v1.2.3", "1.2.3", 0},
		{"1.2.3-rc.1", "1.2.3", 0}, // pre-release ignored — advisory check
		{"0.9.0", "0.10.0", -1},    // numeric, not lexicographic
		{"1.0.0", "2.0.0", -1},
		{"1.10.0", "1.9.9", 1},
		{"2.0.0", "1.99.99", 1},
		{"", "1.0.0", -1},
		{"garbage", "junk", 0}, // both unparseable → equal (safe for advisory)
	}
	for _, tc := range cases {
		if got := Compare(tc.a, tc.b); got != tc.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
