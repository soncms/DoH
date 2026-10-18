//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// Resolve paths from the executable, never the working directory or a user or
// machine profile. SCM starts services in a different working directory.
func portableRoot() string {
	exe, err := os.Executable()
	if err != nil {
		panic(fmt.Sprintf("locate SonDNS executable: %v", err))
	}
	return filepath.Dir(exe)
}

func embeddedRuntimePath(root string) string { return filepath.Join(root, "runtime", "embedded") }

func insideRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel)
}

func insidePortableRuntime(path string) bool {
	return insideRoot(filepath.Join(portableRoot(), "runtime"), path) && strings.EqualFold(filepath.Base(path), "WinDivert64.sys")
}

func portableConfigPath(root, path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	path = filepath.Clean(path)
	rel, err := filepath.Rel(root, path)
	if err != nil || !filepath.IsLocal(rel) || strings.Contains(rel, ":") || rel == "." {
		return "", errors.New("configuration must be inside the SonDNS portable folder")
	}
	return path, nil
}

func resolvePortableDPIExecutableAt(root, configured string) string {
	if strings.TrimSpace(configured) == "" {
		return ""
	}
	path, err := portableConfigPath(root, configured)
	if err != nil || !strings.EqualFold(filepath.Ext(path), ".exe") {
		return ""
	}
	for current := path; ; current = filepath.Dir(current) {
		if err := rejectPortableReparse(current); err != nil {
			return ""
		}
		if strings.EqualFold(filepath.Clean(current), filepath.Clean(root)) {
			break
		}
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	return path
}

func portableLogPath(configured string) string {
	// An old absolute log path must not make the portable build write back into
	// ProgramData or the old installation. Keep that setting local in memory.
	if !filepath.IsLocal(configured) || strings.Contains(configured, ":") {
		return filepath.Join("data", "sondns.log")
	}
	return filepath.Clean(configured)
}

func savePortableConfig(path string, c Config) error {
	local, err := portableConfigPath(portableRoot(), path)
	if err != nil {
		return err
	}
	if err = ensurePortableDirectory(portableRoot(), filepath.Dir(local)); err != nil {
		return err
	}
	if err = rejectPortableReparse(local); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return SaveConfig(local, c)
}

func portableHelperEnvironment(root string) ([]string, error) {
	temp := filepath.Join(root, "data", "helper", "tmp")
	home := filepath.Join(root, "data", "helper", "home")
	for _, dir := range []string{temp, home} {
		if err := ensurePortableDirectory(root, dir); err != nil {
			return nil, err
		}
	}
	values := map[string]string{"TEMP": temp, "TMP": temp, "TMPDIR": filepath.ToSlash(temp), "HOME": filepath.ToSlash(home)}
	env := make([]string, 0, len(os.Environ())+len(values))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, replace := values[strings.ToUpper(key)]; !replace {
			env = append(env, entry)
		}
	}
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	return env, nil
}

func rejectPortableReparse(path string) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attrs, err := windows.GetFileAttributes(p)
	if err != nil {
		return err
	}
	if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("reparse point is not allowed in portable app files: %s", path)
	}
	return nil
}

// Check each existing directory before traversing it. Do not follow a junction
// from a portable folder into an unrelated directory while extracting as admin.
func ensurePortableDirectory(root, path string) error {
	if !insideRoot(root, path) {
		return errors.New("directory escapes the portable folder")
	}
	if err := rejectPortableReparse(root); err != nil {
		return err
	}
	rel, _ := filepath.Rel(root, path)
	current := root
	if rel == "." {
		return nil
	}
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		current = filepath.Join(current, part)
		if err := os.Mkdir(current, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		if err := rejectPortableReparse(current); err != nil {
			return err
		}
	}
	return nil
}
