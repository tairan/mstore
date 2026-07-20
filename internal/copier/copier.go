package copier

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/chieworks/mstore/internal/manifest"
)

// CopyInventory copies regular files using resumable .part files.
// Provider symlinks are intentionally opened and copied as regular files.
func CopyInventory(src, dst string, files []manifest.File) error {
	for _, item := range files {
		from := filepath.Join(src, filepath.FromSlash(item.Path))
		to := filepath.Join(dst, filepath.FromSlash(item.Path))
		if err := copyOne(from, to, item.Size); err != nil {
			return fmt.Errorf("copy %s: %w", item.Path, err)
		}
	}
	return nil
}

func copyOne(src, dst string, size int64) error {
	if info, err := os.Stat(dst); err == nil && info.Mode().IsRegular() && info.Size() == size {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	part := dst + ".part"
	out, err := os.OpenFile(part, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	offset, err := out.Seek(0, io.SeekEnd)
	if err != nil {
		out.Close()
		return err
	}
	if offset > size {
		if err := out.Truncate(0); err != nil {
			out.Close()
			return err
		}
		offset = 0
	}
	if _, err := in.Seek(offset, io.SeekStart); err != nil {
		out.Close()
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	info, err := os.Stat(part)
	if err != nil {
		return err
	}
	if info.Size() != size {
		return fmt.Errorf("size changed during copy: expected %d, got %d", size, info.Size())
	}
	return os.Rename(part, dst)
}
