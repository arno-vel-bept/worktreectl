// Package update installs checksum-verified releases without shelling out or
// modifying project files. The old executable survives every failed download.
package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/arno-vel-bept/worktreectl/internal/diagnostic"
)

const DefaultRepo = "arno-vel-bept/worktreectl"
const maxArchive = 128 << 20
const maxBinary = 64 << 20

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func ValidRepo(value string) bool { return repoPattern.MatchString(value) }

type Options struct {
	Repo, Version, Path string
	Check               bool
}

type Result struct {
	CurrentVersion string `json:"current_version"`
	TargetVersion  string `json:"target_version"`
	Path           string `json:"path"`
	Available      bool   `json:"available"`
	Updated        bool   `json:"updated"`
}

type client struct {
	http                    *http.Client
	api, download, os, arch string
}

func Upgrade(ctx context.Context, current string, options Options) (Result, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	u := client{
		http: &http.Client{Transport: transport, Timeout: 2 * time.Minute, CheckRedirect: secureRedirect},
		api:  "https://api.github.com", download: "https://github.com", os: runtime.GOOS, arch: runtime.GOARCH,
	}
	defer transport.CloseIdleConnections()
	return u.upgrade(ctx, current, options)
}

func secureRedirect(req *http.Request, via []*http.Request) error {
	if req.URL.Scheme != "https" {
		return fmt.Errorf("refusing a non-HTTPS download redirect")
	}
	if len(via) >= 10 {
		return fmt.Errorf("too many download redirects")
	}
	return nil
}

func (u client) upgrade(ctx context.Context, current string, options Options) (Result, error) {
	result := Result{CurrentVersion: current}
	if options.Repo == "" {
		options.Repo = DefaultRepo
	}
	if !ValidRepo(options.Repo) || (options.Version != "" && !ValidVersion(options.Version)) {
		return result, fmt.Errorf("invalid release repository or version")
	}
	if (u.os != "darwin" && u.os != "linux") || (u.arch != "arm64" && u.arch != "amd64") {
		return result, diagnostic.New("no release binary is available for this platform", "Releases support macOS/Linux on amd64/arm64; build from source for other targets.", nil)
	}
	path, err := filepath.EvalSymlinks(options.Path)
	if err != nil {
		return result, diagnostic.New("cannot locate the installed executable", "Check the executable's path and symlinks before upgrading.", err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return result, err
	}
	result.Path = path
	original, err := os.Lstat(path)
	if err != nil || !original.Mode().IsRegular() {
		return result, diagnostic.New("the upgrade destination is not a regular executable file", "Use the installer with --bin-dir to install a user-owned binary.", err)
	}
	endpoint := u.api + "/repos/" + options.Repo + "/releases/latest"
	if options.Version != "" {
		endpoint = u.api + "/repos/" + options.Repo + "/releases/tags/" + options.Version
	}
	var release struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name string `json:"name"`
		} `json:"assets"`
	}
	var metadata strings.Builder
	if err := u.fetch(ctx, endpoint, &metadata, 1<<20); err != nil {
		return result, err
	}
	if err := json.Unmarshal([]byte(metadata.String()), &release); err != nil {
		return result, diagnostic.New("GitHub returned invalid release information", "Retry later or install an explicit version with install.sh.", err)
	}
	if !ValidVersion(release.Tag) || release.Draft || (options.Version == "" && release.Prerelease) || (options.Version != "" && release.Tag != options.Version) {
		return result, diagnostic.New("the selected release is not a published versioned release", "Select a published tag with --version vX.Y.Z.", nil)
	}
	result.TargetVersion = release.Tag
	asset := fmt.Sprintf("worktreectl_%s_%s_%s.tar.gz", strings.TrimPrefix(release.Tag, "v"), u.os, u.arch)
	have := map[string]bool{}
	for _, a := range release.Assets {
		have[a.Name] = true
	}
	if !have[asset] || !have["checksums.txt"] {
		return result, diagnostic.New("the selected release is missing its binary or checksums", "Wait for release uploads to finish, or select another published version.", nil)
	}
	comparison, comparable := compareVersions("v"+strings.TrimPrefix(current, "v"), release.Tag)
	result.Available = !comparable || comparison < 0 || (options.Version != "" && comparison > 0)
	if options.Check || !result.Available {
		return result, nil
	}
	base := u.download + "/" + options.Repo + "/releases/download/" + release.Tag
	var checksums strings.Builder
	if err := u.fetch(ctx, base+"/checksums.txt", &checksums, 1<<20); err != nil {
		return result, err
	}
	expected, err := checksum(checksums.String(), asset)
	if err != nil {
		return result, err
	}
	archive, err := os.CreateTemp(filepath.Dir(path), ".worktreectl-download-*")
	if err != nil {
		return result, diagnostic.New("the executable's install directory is not writable", "Reinstall into a user-owned directory with install.sh --bin-dir \"$HOME/.local/bin\". Managed/package-manager installs should be upgraded through their package manager.", err)
	}
	defer os.Remove(archive.Name())
	defer archive.Close()
	digest := sha256.New()
	if err := u.fetch(ctx, base+"/"+asset, io.MultiWriter(archive, digest), maxArchive); err != nil {
		return result, err
	}
	if hex.EncodeToString(digest.Sum(nil)) != expected {
		return result, diagnostic.New("download checksum mismatch; the installed binary was not changed", "Retry the download. Do not install an archive that fails checksum verification.", nil)
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return result, err
	}
	staged, err := os.CreateTemp(filepath.Dir(path), ".worktreectl-upgrade-*")
	if err != nil {
		return result, err
	}
	defer os.Remove(staged.Name())
	defer staged.Close()
	if err := extractBinary(archive, staged); err != nil {
		return result, diagnostic.New("the release archive does not contain a valid worktreectl binary; nothing was changed", "Report the broken release archive or install another version.", err)
	}
	if err := staged.Chmod(0755); err != nil {
		return result, err
	}
	if err := staged.Sync(); err != nil {
		return result, err
	}
	if err := staged.Close(); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, diagnostic.New("upgrade canceled; the installed binary was not changed", "Retry the upgrade when ready.", err)
	}
	latest, err := os.Lstat(path)
	if err != nil || !os.SameFile(original, latest) {
		return result, diagnostic.New("the executable changed while the upgrade was downloading; nothing was replaced", "Check worktreectl version and retry if an upgrade is still needed.", err)
	}
	if err := os.Rename(staged.Name(), path); err != nil {
		return result, diagnostic.New("could not replace the installed executable", "Check install-directory permissions; the previous binary was left intact.", err)
	}
	result.Updated = true
	return result, nil
}

func (u client) fetch(ctx context.Context, url string, destination io.Writer, limit int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if req.URL.Scheme != "https" {
		return fmt.Errorf("refusing a non-HTTPS release URL")
	}
	req.Header.Set("User-Agent", "worktreectl-upgrade")
	req.Header.Set("Accept", "application/octet-stream")
	if strings.HasPrefix(url, u.api+"/") {
		req.Header.Set("Accept", "application/vnd.github+json")
	}
	response, err := u.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return diagnostic.New("upgrade canceled; the installed binary was not changed", "Retry the upgrade when ready.", ctx.Err())
		}
		return diagnostic.New("could not reach the release server", "Check your network connection and retry; the installed binary was not changed.", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		hint := "Check the published releases and retry; the installed binary was not changed."
		if response.StatusCode == 403 && response.Header.Get("X-RateLimit-Remaining") == "0" {
			hint = "GitHub's anonymous API rate limit was reached. Retry later, or rerun the installer with an explicit --version."
		}
		return diagnostic.New(fmt.Sprintf("release request failed (HTTP %d): %s", response.StatusCode, url), hint, nil)
	}
	n, err := io.Copy(destination, io.LimitReader(response.Body, limit+1))
	if err != nil {
		return diagnostic.New("the release download did not finish; nothing was installed", "Retry when your network connection is stable.", err)
	}
	if n > limit {
		return fmt.Errorf("release download exceeded its size limit; nothing was installed")
	}
	return nil
}

func checksum(text, asset string) (string, error) {
	result := ""
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != asset {
			continue
		}
		decoded, err := hex.DecodeString(fields[0])
		if err != nil || len(decoded) != sha256.Size || result != "" {
			return "", fmt.Errorf("missing, duplicate, or invalid checksum for %s", asset)
		}
		result = strings.ToLower(fields[0])
	}
	if result == "" {
		return "", fmt.Errorf("no checksum was published for %s", asset)
	}
	return result, nil
}

func extractBinary(archive io.Reader, destination io.Writer) error {
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return err
	}
	defer gz.Close()
	limited := &io.LimitedReader{R: gz, N: maxArchive + 1}
	reader := tar.NewReader(limited)
	found := false
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if header.Name != "worktreectl" {
			continue // Never extract arbitrary archive paths or follow links.
		}
		if found || header.Typeflag != tar.TypeReg || header.Size <= 0 || header.Size > maxBinary {
			return fmt.Errorf("invalid or duplicate worktreectl archive entry")
		}
		found = true
		if _, err := io.Copy(destination, reader); err != nil {
			return err
		}
	}
	if !found {
		return fmt.Errorf("worktreectl archive entry is missing")
	}
	if _, err := io.Copy(io.Discard, limited); err != nil {
		return err
	}
	if limited.N <= 0 {
		return fmt.Errorf("expanded release archive exceeded its size limit")
	}
	return nil
}
