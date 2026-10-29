package cli

import (
	"strings"
	"testing"
)

func TestUpgradeHelpAndValidationDoNotNeedARepository(t *testing.T) {
	t.Chdir(t.TempDir())
	out, _ := run(t, 0, "upgrade", "--help")
	if !strings.Contains(out, "--check") || !strings.Contains(out, "Works outside Git repositories") {
		t.Fatal(out)
	}
	for _, args := range [][]string{
		{"upgrade", "--version", "latest", "--json"},
		{"upgrade", "--repo", "invalid", "--json"},
		{"upgrade", "extra", "--json"},
	} {
		out, _ := run(t, 2, args...)
		if strings.Contains(out, "not in a Git repository") {
			t.Fatal(out)
		}
		decode(t, out)
	}
}
