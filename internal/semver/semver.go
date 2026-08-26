// Package semver implements the minimal numeric version comparison the agent
// needs for the server's min_agent_version floor. Deliberately tiny instead
// of a dependency: the agent has a zero-dependency policy (see README), and
// the floor check is advisory only — a warning, never an exit.
package semver

import (
	"strconv"
	"strings"
)

// Compare returns -1, 0 or 1 comparing a and b as dotted numeric versions
// ("1.2.3"). A leading "v" and anything from the first "-" or "+" on
// (pre-release / build metadata) are ignored. Missing segments count as 0,
// so "1.2" == "1.2.0". Unparseable segments compare as 0; two garbage
// versions therefore compare equal, which is the safe outcome for an
// advisory check.
func Compare(a, b string) int {
	as := split(a)
	bs := split(b)
	for i := 0; i < len(as) || i < len(bs); i++ {
		av, bv := 0, 0
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	return 0
}

func split(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ".")
	out := make([]int, len(parts))
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			n = 0
		}
		out[i] = n
	}
	return out
}
