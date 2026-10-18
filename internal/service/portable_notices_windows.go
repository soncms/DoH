//go:build windows

package service

import (
	"fmt"
	"io/fs"
	"path/filepath"

	resources "sondns"
)

func EnsurePortableNoticesAt(root string) error {
	return fs.WalkDir(resources.PortableNotices, ".", func(name string, entry fs.DirEntry, walkErr error) error {
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
		data, err := resources.PortableNotices.ReadFile(name)
		if err != nil {
			return err
		}
		if err := writePortableFile(destination, data, 0o644); err != nil {
			return fmt.Errorf("extract license %s: %w", name, err)
		}
		return nil
	})
}
