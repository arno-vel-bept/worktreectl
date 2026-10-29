package update

import (
	"regexp"
	"strconv"
	"strings"
)

var versionPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)

type version struct {
	numbers [3]uint64
	pre     []string
}

func parseVersion(text string) (version, bool) {
	parts := versionPattern.FindStringSubmatch(text)
	result := version{}
	if parts == nil {
		return result, false
	}
	for i := range result.numbers {
		value, err := strconv.ParseUint(parts[i+1], 10, 64)
		if err != nil {
			return result, false
		}
		result.numbers[i] = value
	}
	if parts[4] != "" {
		result.pre = strings.Split(parts[4], ".")
		for _, id := range result.pre {
			if numeric(id) && len(id) > 1 && id[0] == '0' {
				return result, false
			}
		}
	}
	return result, true
}

func ValidVersion(text string) bool { _, ok := parseVersion(text); return ok }

func compareVersions(a, b string) (int, bool) {
	left, lok := parseVersion(a)
	right, rok := parseVersion(b)
	if !lok || !rok {
		return 0, false
	}
	for i := range left.numbers {
		if left.numbers[i] < right.numbers[i] {
			return -1, true
		}
		if left.numbers[i] > right.numbers[i] {
			return 1, true
		}
	}
	if len(left.pre) == 0 && len(right.pre) != 0 {
		return 1, true
	}
	if len(right.pre) == 0 && len(left.pre) != 0 {
		return -1, true
	}
	for i := 0; i < len(left.pre) && i < len(right.pre); i++ {
		l, r := left.pre[i], right.pre[i]
		if l == r {
			continue
		}
		ln, rn := numeric(l), numeric(r)
		if (ln && !rn) || (ln && rn && len(l) < len(r)) {
			return -1, true
		}
		if (!ln && rn) || (ln && rn && len(l) > len(r)) {
			return 1, true
		}
		return strings.Compare(l, r), true
	}
	if len(left.pre) < len(right.pre) {
		return -1, true
	}
	if len(left.pre) > len(right.pre) {
		return 1, true
	}
	return 0, true
}

func numeric(text string) bool { return strings.Trim(text, "0123456789") == "" }
