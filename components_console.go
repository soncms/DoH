//go:build windows && console

package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

type installedComponent struct {
	Version       string            `json:"version"`
	Source        string            `json:"source"`
	ArchiveSHA256 string            `json:"archive_sha256,omitempty"`
	Dir           string            `json:"directory"`
	Files         map[string]string `json:"files"`
}
type componentLock struct {
	Schema      int                           `json:"schema"`
	InstalledAt time.Time                     `json:"installed_at"`
	Components  map[string]installedComponent `json:"components"`
}
type upstreamRelease struct {
	Tag    string `json:"tag_name"`
	Assets []struct {
		Name   string `json:"name"`
		URL    string `json:"browser_download_url"`
		Digest string `json:"digest"`
	} `json:"assets"`
}
type upstreamVersions struct {
	DNS    upstreamRelease
	WD     upstreamRelease
	Bundle string
}

func lockPath(root string) string { return filepath.Join(root, "data", "components.lock.json") }
func readJSONFile(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
func writeJSONAtomic(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(b, '\n'))
}
func writeAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".sondns-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}
func loadComponents(root string) (componentLock, error) {
	var result componentLock
	err := readJSONFile(lockPath(root), &result)
	if err != nil {
		return result, fmt.Errorf("runtime unavailable: %w; use Components > Install/update", err)
	}
	if result.Schema != 1 {
		return result, errors.New("unsupported component lock schema")
	}
	return result, nil
}
func componentPath(root string, c installedComponent) (string, error) {
	if c.Dir == "" || !filepath.IsLocal(filepath.FromSlash(c.Dir)) || strings.Contains(c.Dir, ":") {
		return "", errors.New("invalid component directory")
	}
	p := filepath.Join(root, filepath.FromSlash(c.Dir))
	if !insideRoot(filepath.Join(root, "runtime"), p) {
		return "", errors.New("component path escapes runtime directory")
	}
	return p, nil
}
func hashBytes(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func verifyInstalled(root string, lock componentLock) error {
	if lock.Schema != 1 {
		return errors.New("unsupported component lock schema")
	}
	if err := rejectReparseTree(filepath.Join(root, "runtime")); err != nil {
		return err
	}
	for _, name := range []string{"dnsproxy", "zapret2", "windivert"} {
		c, ok := lock.Components[name]
		if !ok || len(c.Files) == 0 {
			return fmt.Errorf("missing component %s", name)
		}
		dir, err := componentPath(root, c)
		if err != nil {
			return err
		}
		for rel, want := range c.Files {
			if !filepath.IsLocal(filepath.FromSlash(rel)) || strings.Contains(rel, ":") {
				return errors.New("invalid asset path")
			}
			p := filepath.Join(dir, filepath.FromSlash(rel))
			if !insideRoot(dir, p) {
				return errors.New("asset path escapes component")
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if len(want) != 64 || hashBytes(b) != want {
				return fmt.Errorf("SHA-256 mismatch: %s/%s", name, rel)
			}
		}
		required := map[string][]string{"dnsproxy": {"dnsproxy.exe"}, "windivert": {"WinDivert.dll", "WinDivert64.sys"}, "zapret2": {"winws2.exe", "cygwin1.dll", "WinDivert.dll", "WinDivert64.sys", "lua/zapret-lib.lua", "lua/zapret-antidpi.lua"}}
		for _, f := range required[name] {
			if c.Files[f] == "" {
				return fmt.Errorf("missing required asset %s/%s", name, f)
			}
		}
	}
	return nil
}

// Never follow a junction while checking or protecting executable runtime files.
func rejectReparseTree(root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		p, err := windows.UTF16PtrFromString(path)
		if err != nil {
			return err
		}
		attrs, err := windows.GetFileAttributes(p)
		if err != nil {
			return err
		}
		if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return fmt.Errorf("reparse point is not allowed in installation: %s", path)
		}
		return nil
	})
}
func fetchUpstream(ctx context.Context, url string, limit int64) ([]byte, error) {
	if !strings.HasPrefix(url, "https://") {
		return nil, errors.New("upstream downloads require HTTPS")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "SonDNS-Portable/"+buildVersion)
	client := &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return errors.New("upstream redirect requires HTTPS")
		}
		if len(via) >= 10 {
			return errors.New("too many upstream redirects")
		}
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream HTTP %d from %s", resp.StatusCode, url)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err == nil && int64(len(b)) > limit {
		err = errors.New("upstream artifact exceeds size limit")
	}
	return b, err
}
func githubJSON(ctx context.Context, path string, v any) error {
	b, err := fetchUpstream(ctx, "https://api.github.com/repos/"+path, 8<<20)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
func latestUpstreams(ctx context.Context) (upstreamVersions, error) {
	var v upstreamVersions
	if err := githubJSON(ctx, "AdguardTeam/dnsproxy/releases/latest", &v.DNS); err != nil {
		return v, err
	}
	if err := githubJSON(ctx, "basil00/WinDivert/releases/latest", &v.WD); err != nil {
		return v, err
	}
	var commit struct {
		SHA string `json:"sha"`
	}
	err := githubJSON(ctx, "bol-van/zapret-win-bundle/commits/master", &commit)
	v.Bundle = commit.SHA
	if err == nil {
		b, decodeErr := hex.DecodeString(v.Bundle)
		if decodeErr != nil || len(b) != 20 {
			err = errors.New("invalid upstream bundle commit")
		}
	}
	return v, err
}
func showComponents(root string, out io.Writer) error {
	lock, err := loadComponents(root)
	if err != nil {
		return err
	}
	for _, name := range []string{"dnsproxy", "zapret2", "windivert"} {
		c := lock.Components[name]
		fmt.Fprintf(out, "%-10s %s\n  %s\n", name, c.Version, c.Source)
	}
	return verifyInstalled(root, lock)
}
func checkComponents(ctx context.Context, root string, out io.Writer) (upstreamVersions, error) {
	v, err := latestUpstreams(ctx)
	if err != nil {
		return v, err
	}
	lock, _ := loadComponents(root)
	for _, row := range [][2]string{{"dnsproxy", v.DNS.Tag}, {"zapret2", v.Bundle}, {"windivert", v.WD.Tag}} {
		old := lock.Components[row[0]].Version
		if old == "" {
			old = "not installed"
		}
		fmt.Fprintf(out, "%-10s %s -> %s\n", row[0], old, row[1])
	}
	return v, nil
}
func installRelease(ctx context.Context, root, transaction, name string, r upstreamRelease, selectAsset func(string) bool, selectFile func(string) string) (installedComponent, error) {
	c := installedComponent{Version: r.Tag, Files: map[string]string{}}
	var archive []byte
	for _, asset := range r.Assets {
		if !selectAsset(asset.Name) {
			continue
		}
		if !strings.HasPrefix(asset.URL, "https://github.com/") {
			return c, errors.New("unexpected release URL")
		}
		var err error
		archive, err = fetchUpstream(ctx, asset.URL, 64<<20)
		if err != nil {
			return c, err
		}
		c.Source = asset.URL
		c.ArchiveSHA256 = hashBytes(archive)
		if asset.Digest != "" && asset.Digest != "sha256:"+c.ArchiveSHA256 {
			return c, errors.New("upstream archive digest mismatch")
		}
		break
	}
	if len(archive) == 0 {
		return c, fmt.Errorf("no Windows amd64 asset in upstream %s %s", name, r.Tag)
	}
	c.Dir = filepath.ToSlash(filepath.Join("runtime", name, transaction))
	dir, _ := componentPath(root, c)
	z, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return c, err
	}
	for _, f := range z.File {
		rel := selectFile(filepath.ToSlash(f.Name))
		if rel == "" || f.FileInfo().IsDir() {
			continue
		}
		if err = extractComponentFile(dir, rel, f, &c); err != nil {
			return c, err
		}
	}
	if len(c.Files) == 0 {
		return c, errors.New("release archive has no expected runtime files")
	}
	return c, nil
}
func extractComponentFile(dir, rel string, f *zip.File, c *installedComponent) error {
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if !insideRoot(dir, p) {
		return errors.New("archive path escapes runtime")
	}
	reader, err := f.Open()
	if err != nil {
		return err
	}
	b, err := io.ReadAll(io.LimitReader(reader, 32<<20))
	reader.Close()
	if err != nil {
		return err
	}
	if len(b) >= 32<<20 {
		return errors.New("runtime file too large")
	}
	if err = writeAtomic(p, b); err == nil {
		c.Files[rel] = hashBytes(b)
	}
	return err
}
func prepareComponents(ctx context.Context, root string, v upstreamVersions, out io.Writer) (componentLock, error) {
	transaction := time.Now().UTC().Format("20060102T150405.000000000")
	result := componentLock{Schema: 1, InstalledAt: time.Now().UTC(), Components: map[string]installedComponent{}}
	fmt.Fprintln(out, "Downloading official dnsproxy Windows amd64...")
	dnsComponent, err := installRelease(ctx, root, transaction, "dnsproxy", v.DNS, func(n string) bool { return strings.Contains(n, "windows-amd64") && strings.HasSuffix(n, ".zip") }, func(n string) string {
		base := filepath.Base(n)
		if base == "dnsproxy.exe" || base == "LICENSE" || base == "README.md" {
			return base
		}
		return ""
	})
	if err != nil {
		return result, err
	}
	result.Components["dnsproxy"] = dnsComponent
	fmt.Fprintln(out, "Downloading original WinDivert DLL/SYS...")
	wd, err := installRelease(ctx, root, transaction, "windivert", v.WD, func(n string) bool { return strings.HasPrefix(n, "WinDivert-") && strings.HasSuffix(n, ".zip") }, func(n string) string {
		if strings.Contains(n, "/x64/") && (strings.HasSuffix(n, "/WinDivert.dll") || strings.HasSuffix(n, "/WinDivert64.sys")) {
			return filepath.Base(n)
		}
		if strings.HasSuffix(n, "/LICENSE") {
			return "LICENSE"
		}
		return ""
	})
	if err != nil {
		return result, err
	}
	result.Components["windivert"] = wd
	wdDir, _ := componentPath(root, wd)
	if err = verifyDriverSignature(filepath.Join(wdDir, "WinDivert64.sys")); err != nil {
		return result, fmt.Errorf("driver signature: %w", err)
	}
	fmt.Fprintln(out, "Downloading unmodified zapret2 Windows runtime from commit "+v.Bundle+"...")
	var tree struct {
		Truncated bool `json:"truncated"`
		Items     []struct {
			Path string `json:"path"`
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"tree"`
	}
	if err = githubJSON(ctx, "bol-van/zapret-win-bundle/git/trees/"+v.Bundle+"?recursive=1", &tree); err != nil {
		return result, err
	}
	if tree.Truncated {
		return result, errors.New("upstream tree is truncated")
	}
	zap := installedComponent{Version: v.Bundle, Source: "https://github.com/bol-van/zapret-win-bundle/tree/" + v.Bundle + "/zapret-winws", Dir: filepath.ToSlash(filepath.Join("runtime", "zapret2", transaction)), Files: map[string]string{}}
	zapDir, _ := componentPath(root, zap)
	for _, f := range tree.Items {
		if f.Type != "blob" || !strings.HasPrefix(f.Path, "zapret-winws/") {
			continue
		}
		rel := strings.TrimPrefix(f.Path, "zapret-winws/")
		if rel != "winws2.exe" && rel != "cygwin1.dll" && !strings.HasPrefix(rel, "lua/") && !strings.HasPrefix(rel, "windivert.filter/") {
			continue
		}
		if len(zap.Files) >= 128 {
			return result, errors.New("too many runtime assets")
		}
		b, fetchErr := fetchUpstream(ctx, "https://raw.githubusercontent.com/bol-van/zapret-win-bundle/"+v.Bundle+"/"+f.Path, 16<<20)
		if fetchErr != nil {
			return result, fetchErr
		}
		blob := sha1.New()
		fmt.Fprintf(blob, "blob %d%c", len(b), 0)
		blob.Write(b)
		if hex.EncodeToString(blob.Sum(nil)) != f.SHA {
			return result, errors.New("upstream Git blob hash mismatch")
		}
		p := filepath.Join(zapDir, filepath.FromSlash(rel))
		if !insideRoot(zapDir, p) {
			return result, errors.New("unsafe upstream path")
		}
		if err = writeAtomic(p, b); err != nil {
			return result, err
		}
		zap.Files[rel] = hashBytes(b)
	}
	for _, name := range []string{"WinDivert.dll", "WinDivert64.sys"} {
		b, readErr := os.ReadFile(filepath.Join(wdDir, name))
		if readErr != nil {
			return result, readErr
		}
		if err = writeAtomic(filepath.Join(zapDir, name), b); err != nil {
			return result, err
		}
		zap.Files[name] = hashBytes(b)
	}
	result.Components["zapret2"] = zap
	for _, required := range []string{"winws2.exe", "cygwin1.dll", "lua/zapret-lib.lua", "lua/zapret-antidpi.lua"} {
		if zap.Files[required] == "" {
			return result, fmt.Errorf("upstream missing %s", required)
		}
	}
	return result, verifyInstalled(root, result)
}
