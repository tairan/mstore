package fsutil

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chieworks/mstore/internal/manifest"
)

var metadata = map[string]bool{".mstore.json": true, ".msc": true, ".mdl": true, ".mv": true}

func IsTemporary(name string) bool {
	base := filepath.Base(name)
	return strings.HasSuffix(base, ".part") || strings.HasSuffix(base, ".tmp") ||
		strings.HasSuffix(base, ".incomplete") || strings.HasPrefix(base, ".~")
}

// Scan returns a stable inventory and rejects unsafe or incomplete source trees.
func Scan(root string, fullHash bool) ([]manifest.File, int64, error) {
	var files []manifest.File
	var bytes int64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if IsTemporary(rel) {
			return fmt.Errorf("temporary or incomplete file found: %s", rel)
		}
		if d.IsDir() {
			return nil
		}
		if metadata[rel] || metadata[filepath.Base(rel)] {
			return nil
		}
		info, err := os.Stat(path) // follows provider symlinks and rejects dangling links
		if err != nil {
			return fmt.Errorf("invalid source entry %s: %w", rel, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported source entry %s (%s)", rel, info.Mode())
		}
		item := manifest.File{Path: filepath.ToSlash(rel), Size: info.Size()}
		if fullHash {
			item.SHA256, err = HashFile(path)
			if err != nil {
				return err
			}
		}
		files = append(files, item)
		bytes += info.Size()
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	if len(files) == 0 {
		return nil, 0, fmt.Errorf("model contains no files")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, bytes, nil
}

func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func SameTree(a, b []manifest.File, compareHashes bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Path != b[i].Path || a[i].Size != b[i].Size {
			return false
		}
		if compareHashes && a[i].SHA256 != b[i].SHA256 {
			return false
		}
	}
	return true
}

func SyncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
