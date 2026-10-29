package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T, mode string) (client, Options, <-chan string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bin with spaces", "worktreectl")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(canonical, "worktreectl")
	if err := os.WriteFile(path, []byte("old binary"), 0755); err != nil {
		t.Fatal(err)
	}
	asset := "worktreectl_1.2.3_darwin_arm64.tar.gz"
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	name := "worktreectl"
	typeflag := byte(tar.TypeReg)
	if mode == "traversal" {
		name = "../worktreectl"
	}
	if mode == "symlink" {
		typeflag = tar.TypeSymlink
	}
	payload := []byte("new verified binary")
	size := int64(len(payload))
	if typeflag == tar.TypeSymlink {
		size = 0
	}
	for i := 0; i < 1+boolInt(mode == "duplicate binary"); i++ {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0755, Typeflag: typeflag, Linkname: "/untrusted", Size: size}); err != nil {
			t.Fatal(err)
		}
		if size > 0 {
			if _, err := tw.Write(payload); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	archiveBytes := archive.Bytes()
	if mode == "invalid gzip" {
		archiveBytes = []byte("not a gzip archive")
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(archiveBytes))
	if mode == "bad checksum" {
		digest = strings.Repeat("0", 64)
	}
	checksums := digest + "  " + asset + "\n"
	if mode == "duplicate checksum" {
		checksums += checksums
	}
	if mode == "missing checksum" {
		checksums = "unrelated\n"
	}
	requests := make(chan string, 10)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.URL.Path
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/"):
			if mode == "API failure" {
				w.Header().Set("X-RateLimit-Remaining", "0")
				w.WriteHeader(403)
				return
			}
			assets := []map[string]string{{"name": asset}, {"name": "checksums.txt"}}
			if mode == "missing asset" {
				assets = assets[1:]
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v1.2.3", "draft": mode == "draft", "prerelease": mode == "prerelease", "assets": assets})
		case strings.HasSuffix(r.URL.Path, "/checksums.txt"):
			fmt.Fprint(w, checksums)
		case strings.HasSuffix(r.URL.Path, "/"+asset):
			if mode == "executable changed" {
				if err := os.WriteFile(path+".concurrent", []byte("concurrent binary"), 0755); err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				if err := os.Rename(path+".concurrent", path); err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
			}
			if mode == "download failure" {
				w.WriteHeader(404)
				return
			}
			if mode == "HTTP redirect" {
				w.Header().Set("Location", "http://example.invalid/archive")
				w.WriteHeader(http.StatusFound)
				return
			}
			_, _ = w.Write(archiveBytes)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	httpClient := server.Client()
	httpClient.CheckRedirect = secureRedirect
	u := client{http: httpClient, api: server.URL + "/api", download: server.URL, os: "darwin", arch: "arm64"}
	return u, Options{Repo: "example/worktreectl", Path: path}, requests
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TestVerifiedUpgradeReplacesBinaryAndCleansTemps(t *testing.T) {
	u, options, _ := fixture(t, "valid")
	result, err := u.upgrade(context.Background(), "0.1.0", options)
	if err != nil || !result.Updated || !result.Available || result.TargetVersion != "v1.2.3" {
		t.Fatal(result, err)
	}
	data, err := os.ReadFile(options.Path)
	if err != nil || string(data) != "new verified binary" {
		t.Fatal(string(data), err)
	}
	info, err := os.Stat(options.Path)
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatal(info, err)
	}
	assertNoTemps(t, options.Path)
}

func TestFailedUpgradePreservesInstalledBinary(t *testing.T) {
	for _, mode := range []string{"API failure", "download failure", "bad checksum", "duplicate checksum", "missing checksum", "missing asset", "symlink", "traversal", "duplicate binary", "invalid gzip", "HTTP redirect", "draft", "prerelease"} {
		t.Run(mode, func(t *testing.T) {
			u, options, _ := fixture(t, mode)
			if _, err := u.upgrade(context.Background(), "0.1.0", options); err == nil {
				t.Fatal("unexpected success")
			}
			data, err := os.ReadFile(options.Path)
			if err != nil || string(data) != "old binary" {
				t.Fatal("old binary was changed", string(data), err)
			}
			assertNoTemps(t, options.Path)
		})
	}
}

func TestCheckAndCurrentVersionsNeverDownloadOrDowngrade(t *testing.T) {
	for _, tc := range []struct {
		current          string
		check, available bool
	}{
		{"0.1.0", true, true}, {"1.2.3", false, false}, {"2.0.0", false, false},
	} {
		u, options, requests := fixture(t, "valid")
		options.Check = tc.check
		result, err := u.upgrade(context.Background(), tc.current, options)
		if err != nil || result.Updated || result.Available != tc.available {
			t.Fatal(result, err)
		}
		if len(requests) != 1 {
			t.Fatal("downloaded an archive during a check/no-op")
		}
		assertNoTemps(t, options.Path)
	}
}

func TestExplicitVersionAndExecutableSymlinks(t *testing.T) {
	u, options, requests := fixture(t, "valid")
	actual := options.Path
	alias := filepath.Join(filepath.Dir(actual), "alias")
	if err := os.Symlink(actual, alias); err != nil {
		t.Fatal(err)
	}
	options.Path, options.Version = alias, "v1.2.3"
	result, err := u.upgrade(context.Background(), "2.0.0", options)
	if err != nil || !result.Updated || result.Path != actual {
		t.Fatal(result, err)
	}
	if request := <-requests; !strings.HasSuffix(request, "/releases/tags/v1.2.3") {
		t.Fatal(request)
	}
	info, err := os.Lstat(alias)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("executable alias was overwritten", info, err)
	}
}

func TestCanceledAndConcurrentUpgradesDoNotReplaceBinary(t *testing.T) {
	for _, mode := range []string{"canceled", "executable changed"} {
		u, options, _ := fixture(t, mode)
		ctx, cancel := context.WithCancel(context.Background())
		if mode == "canceled" {
			cancel()
		}
		_, err := u.upgrade(ctx, "0.1.0", options)
		cancel()
		if err == nil || (mode == "canceled" && !errors.Is(err, context.Canceled)) {
			t.Fatal(err)
		}
		want := "old binary"
		if mode == "executable changed" {
			want = "concurrent binary"
			if !strings.Contains(err.Error(), "changed while") {
				t.Fatal(err)
			}
		}
		data, readErr := os.ReadFile(options.Path)
		if readErr != nil || string(data) != want {
			t.Fatal(string(data), readErr)
		}
		assertNoTemps(t, options.Path)
	}
}

func assertNoTemps(t *testing.T, path string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".worktreectl-*"))
	if err != nil || len(files) != 0 {
		t.Fatal("upgrade temporary files were left behind", files, err)
	}
}

func TestVersionOrdering(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"v0.1.0", "v0.2.0", -1}, {"v0.2.0", "v0.2.0", 0}, {"v0.10.0", "v0.2.0", 1},
		{"v1.0.0-rc.1", "v1.0.0", -1}, {"v1.0.0-rc.10", "v1.0.0-rc.2", 1},
		{"v1.0.0-alpha.1", "v1.0.0-alpha", 1}, {"v1.0.0-1", "v1.0.0-alpha", -1},
	} {
		got, ok := compareVersions(tc.a, tc.b)
		if !ok || got != tc.want {
			t.Fatal(tc, got, ok)
		}
	}
	for _, text := range []string{"dev", "v1.2", "v01.2.3", "v1.2.3-rc..1", "v1.2.3-01"} {
		if ValidVersion(text) {
			t.Fatal("accepted invalid version", text)
		}
	}
}
