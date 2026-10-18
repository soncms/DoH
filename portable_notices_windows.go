//go:build windows

package main

import (
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
)

// Keep copyright and license notices with a single-EXE distribution. These
// documents are also available before extraction in the executable itself.
//
//go:embed LICENSE NOTICE THIRD_PARTY_NOTICES.md PORTABLE.md licenses
var portableNotices embed.FS

func ensurePortableNoticesAt(root string) error {
	return fs.WalkDir(portableNotices, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		destination := filepath.Join(root, filepath.FromSlash(name))
		if err := ensurePortableDirectory(root, filepath.Dir(destination)); err != nil {
			return err
		}
		data, err := portableNotices.ReadFile(name)
		if err != nil {
			return err
		}
		if err := writePortableFile(destination, data, 0o644); err != nil {
			return fmt.Errorf("extract license %s: %w", name, err)
		}
		return nil
	})
}
