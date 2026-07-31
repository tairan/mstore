package lock

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

type Lock struct{ path string }

func Acquire(dir, name string, timeout time.Duration) (*Lock, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, name+".lock")
	deadline := time.Now().Add(timeout)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
			if syncErr := f.Sync(); syncErr != nil {
				f.Close()
				os.Remove(path)
				return nil, syncErr
			}
			if err := f.Close(); err != nil {
				os.Remove(path)
				return nil, err
			}
			return &Lock{path: path}, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		if stale(path) {
			_ = os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("lock timeout: %s", path)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func stale(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(string(bytesTrimSpace(b)))
	if err != nil || pid <= 0 {
		info, statErr := os.Stat(path)
		return statErr == nil && time.Since(info.ModTime()) > 24*time.Hour
	}
	_, err = os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
	return os.IsNotExist(err)
}

func bytesTrimSpace(b []byte) []byte {
	i, j := 0, len(b)
	for i < j && (b[i] == ' ' || b[i] == '\n' || b[i] == '\r' || b[i] == '\t') {
		i++
	}
	for j > i && (b[j-1] == ' ' || b[j-1] == '\n' || b[j-1] == '\r' || b[j-1] == '\t') {
		j--
	}
	return b[i:j]
}

func (l *Lock) Release() error { return os.Remove(l.path) }

// Active reports whether the PID recorded in a lock file still exists. An
// unreadable or malformed lock is treated as active so callers fail closed.
func Active(path string) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	contents := bytesTrimSpace(b)
	if len(contents) == 0 {
		return probeAdvisory(path)
	}
	pid, err := strconv.Atoi(string(contents))
	if err != nil || pid <= 0 {
		return probeAdvisory(path)
	}
	_, err = os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return true, err
}

func probeAdvisory(path string) (bool, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return true, err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return true, nil
		}
		return true, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		return true, err
	}
	return false, nil
}
