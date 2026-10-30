package worktree

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arno-vel-bept/worktreectl/internal/config"
	"github.com/arno-vel-bept/worktreectl/internal/diagnostic"
	"github.com/arno-vel-bept/worktreectl/internal/repo"
)

func TestBranchDirectoryNamesAreFlatAndBounded(t *testing.T) {
	for _, tc := range []struct{ branch, directory string }{
		{"fixes/my-feature-name", "fixes-my-feature-name"},
		{"team/fixes/my-feature", "team-fixes-my-feature"},
		{"_fixes/my-feature", "fixes-my-feature"},
		{"fixes/my'feature+$HOME", "fixes-my-feature-HOME"},
		{"fixes/café", "fixes-caf-"},
	} {
		if got := branchDirectoryName(tc.branch); got != tc.directory {
			t.Fatalf("%q -> %q, want %q", tc.branch, got, tc.directory)
		}
	}
	root := t.TempDir()
	for _, branch := range []string{"修复/功能", "fixes/+++", "../../escape", "fixes/" + strings.Repeat("long", 100)} {
		name := branchDirectoryName(branch)
		if !validName.MatchString(name) || len(name) > maxBranchDirectoryName || filepath.Dir(filepath.Join(root, name)) != root {
			t.Fatal("unsafe directory name", branch, name)
		}
		if branchDirectoryName(branch) != name {
			t.Fatal("directory mapping is not deterministic")
		}
	}
	a, b := "team/"+strings.Repeat("a", 300), "team/"+strings.Repeat("a", 300)+"b"
	if branchDirectoryName(a) == branchDirectoryName(b) || branchDirectoryName("修复/功能") == branchDirectoryName("修复/其他") {
		t.Fatal("truncated or non-ASCII branch names lost their distinguishing hash")
	}
}

func TestCreationNamesSeparateDirectoryFromBranch(t *testing.T) {
	service := &Service{Repo: &repo.Repo{Main: t.TempDir()}, Config: config.Defaults()}
	for _, tc := range []struct {
		options      NewOptions
		name, branch string
	}{
		{NewOptions{Name: "feature"}, "feature", "wt/feature"},
		{NewOptions{Name: "fixes/feature"}, "fixes-feature", "fixes/feature"},
		{NewOptions{Name: "custom", Branch: "fixes/feature"}, "custom", "fixes/feature"},
		{NewOptions{Name: "fixes/feature", Branch: "fixes/feature"}, "fixes-feature", "fixes/feature"},
		{NewOptions{Name: "detached", Detach: true}, "detached", ""},
	} {
		name, branch, err := service.creationNames(context.Background(), tc.options)
		if err != nil || name != tc.name || branch != tc.branch {
			t.Fatalf("%+v -> %q, %q (%v)", tc.options, name, branch, err)
		}
	}
	for _, options := range []NewOptions{
		{Name: "fixes/feature", Detach: true},
		{Name: "fixes/feature", Branch: "other/feature"},
		{Name: "bad name"},
		{Name: "fixes/../escape"},
		{Name: "fixes//feature"},
		{Name: "fixes/feature.lock"},
		{Name: "custom", Branch: "-fixes/feature"},
	} {
		if _, _, err := service.creationNames(context.Background(), options); err == nil || diagnostic.Hint(err) == "" {
			t.Fatalf("accepted invalid options or omitted recovery hint: %+v (%v)", options, err)
		}
	}
}
