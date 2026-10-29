package repo

import (
	"path/filepath"
	"testing"
)

func TestParseTreesPreservesPaths(t *testing.T) {
	out := "worktree /project with space\x00HEAD abc\x00branch refs/heads/main\x00\x00worktree /trees/new\nline\x00HEAD def\x00detached\x00locked busy\x00\x00"
	trees := ParseTrees([]byte(out))
	if len(trees) != 2 || !trees[0].Main || trees[1].Main || trees[1].Path != "/trees/new\nline" || trees[1].Branch != "" || !trees[1].Locked {
		t.Fatalf("trees: %+v", trees)
	}
}

func TestUnderHasPathBoundaries(t *testing.T) {
	root := t.TempDir()
	if !Under(root, root) || !Under(filepath.Join(root, "nested"), root) || Under(root+"-other", root) || Under(filepath.Dir(root), root) {
		t.Fatal("incorrect containment")
	}
}
