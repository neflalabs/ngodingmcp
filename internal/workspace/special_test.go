//go:build !windows

package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestFIFOSpecialFileRejected(t *testing.T) {
	ws := newTestWorkspace(t)
	pipe := filepath.Join(ws.Root(), "pipe")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}

	// Opening a FIFO for reading blocks until a writer appears. The tool
	// must refuse the path before it reaches os.Open.
	type result struct{ err error }
	readDone := make(chan result, 1)
	go func() {
		_, err := ws.ReadFile("pipe", 0, 0)
		readDone <- result{err}
	}()
	select {
	case r := <-readDone:
		if !errors.Is(r.err, ErrSpecialFile) {
			t.Fatalf("ReadFile err = %v, want ErrSpecialFile", r.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ReadFile blocked on FIFO")
	}

	if _, _, err := ws.WriteFile("pipe", "x"); !errors.Is(err, ErrSpecialFile) {
		t.Fatalf("WriteFile over FIFO err = %v, want ErrSpecialFile", err)
	}
	info, err := os.Lstat(pipe)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatal("WriteFile replaced the FIFO")
	}

	link := filepath.Join(ws.Root(), "link")
	if err := os.Symlink("pipe", link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, _, err := ws.WriteFile("link", "x"); !errors.Is(err, ErrSpecialFile) {
		t.Fatalf("WriteFile over symlink to FIFO err = %v, want ErrSpecialFile", err)
	}
	if target, err := os.Readlink(link); err != nil || target != "pipe" {
		t.Fatalf("symlink changed: target=%q err=%v", target, err)
	}
	if info, err := os.Lstat(pipe); err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("FIFO behind symlink changed: info=%v err=%v", info, err)
	}
}
