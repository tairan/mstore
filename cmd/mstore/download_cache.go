package main

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const downloadCacheMarker = ".mstore-download-cache"

func downloadCacheRoot() (string, error) {
	cacheHome, err := xdgCacheHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(cacheHome, "mstore", "downloads"), nil
}

func xdgCacheHome() (string, error) {
	path := os.Getenv("XDG_CACHE_HOME")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, ".cache")
	}
	return expandDownloadCachePath(path)
}

func expandDownloadCachePath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("cache path must not be empty")
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	return filepath.Abs(path)
}

func (a *app) cache(args []string) error {
	if len(args) == 0 {
		return usageError("cache requires path or clean")
	}
	switch args[0] {
	case "path":
		if len(args) != 1 {
			return usageError("cache path accepts no arguments")
		}
		path, err := downloadCacheRoot()
		if err != nil {
			return err
		}
		if a.global.json {
			return writeJSON(a.out, map[string]string{"path": path})
		}
		fmt.Fprintln(a.out, path)
		return nil
	case "clean":
		f := newFlags("cache clean")
		path := f.String("path", "", "")
		yes := f.Bool("yes", false, "")
		if err := f.Parse(args[1:]); err != nil {
			return usageError("%v", err)
		}
		if len(f.Args()) != 0 {
			return usageError("cache clean accepts no positional arguments")
		}
		if !*yes {
			return usageError("cache clean requires --yes")
		}
		pathSet := false
		f.Visit(func(flag *flag.Flag) { pathSet = pathSet || flag.Name == "path" })
		target := *path
		if !pathSet {
			var err error
			target, err = downloadCacheRoot()
			if err != nil {
				return err
			}
		}
		removed, err := removeDownloadCache(target)
		if err != nil {
			return err
		}
		return a.success(map[string]string{"removed": removed}, "removed "+removed)
	default:
		return usageError("unknown cache command %q", args[0])
	}
}

func removeDownloadCache(path string) (string, error) {
	target, err := expandDownloadCachePath(path)
	if err != nil {
		return "", err
	}
	if err := safeDownloadCacheTarget(target); err != nil {
		return "", err
	}
	info, err := os.Lstat(target)
	if err != nil {
		return "", fmt.Errorf("inspect download cache: %w", err)
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return "", fmt.Errorf("download cache must be a directory: %s", target)
	}
	marker, err := os.Lstat(filepath.Join(target, downloadCacheMarker))
	if err != nil {
		return "", fmt.Errorf("refusing to clean unmarked download cache %s: %w", target, err)
	}
	if !marker.Mode().IsRegular() || marker.Mode()&fs.ModeSymlink != 0 {
		return "", fmt.Errorf("refusing to clean download cache with invalid marker: %s", target)
	}
	if err := os.RemoveAll(target); err != nil {
		return "", fmt.Errorf("remove download cache %s: %w", target, err)
	}
	return target, nil
}

func safeDownloadCacheTarget(target string) error {
	volumeRoot := filepath.VolumeName(target) + string(filepath.Separator)
	if target == volumeRoot {
		return fmt.Errorf("refusing to clean filesystem root")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	home, err = expandDownloadCachePath(home)
	if err != nil {
		return err
	}
	cacheHome, err := xdgCacheHome()
	if err != nil {
		return err
	}
	if target == home || target == cacheHome {
		return fmt.Errorf("refusing to clean unsafe cache target: %s", target)
	}
	return nil
}
