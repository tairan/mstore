package lock

import (
	"os"
	"syscall"
	"testing"
)

func TestActiveProbesEmptyAdvisoryLock(t *testing.T) {
	path := t.TempDir() + "/download.lock"
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	active, err := Active(path)
	if err != nil || active {
		t.Fatalf("stale marker active=%v err=%v", active, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	active, err = Active(path)
	if err != nil || !active {
		t.Fatalf("held lock active=%v err=%v", active, err)
	}
}
