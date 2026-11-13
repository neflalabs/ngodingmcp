package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSwitchChangesRootAndSettings(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(b, "only-in-b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, ".env"), []byte("SECRET=1"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := New(a, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	rootB, err := ResolveRoot(b)
	if err != nil {
		t.Fatal(err)
	}

	if err := w.Switch(context.Background(), Settings{Root: rootB, MaxRead: 4096}, nil); err != nil {
		t.Fatalf("Switch: %v", err)
	}
	if w.Root() != rootB {
		t.Fatalf("Root = %q, want %q", w.Root(), rootB)
	}
	if w.MaxRead() != 4096 {
		t.Errorf("MaxRead = %d, want 4096", w.MaxRead())
	}
	if _, err := w.ReadFile("only-in-b.txt", 0, 0); err != nil {
		t.Errorf("ReadFile in new root: %v", err)
	}
	// Traversal and the sensitive guard still apply to the new root.
	if _, err := w.ReadFile("../x", 0, 0); err == nil {
		t.Error("traversal out of the new root was allowed")
	}
	if _, err := w.ReadFile(".env", 0, 0); !errors.Is(err, ErrSensitive) {
		t.Errorf("ReadFile(.env) err = %v, want ErrSensitive", err)
	}

	// A per-workspace override turns the guard off; 0 MaxRead means default.
	if err := w.Switch(context.Background(), Settings{Root: rootB, AllowSensitive: true}, nil); err != nil {
		t.Fatal(err)
	}
	if !w.AllowSensitive() {
		t.Error("AllowSensitive should be true after switch")
	}
	if w.MaxRead() != DefaultMaxRead {
		t.Errorf("MaxRead = %d, want default %d", w.MaxRead(), DefaultMaxRead)
	}
	if _, err := w.ReadFile(".env", 0, 0); err != nil {
		t.Errorf("ReadFile(.env) with guard off: %v", err)
	}
}

func TestSwitchWaitsForHold(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	w, err := New(a, 0)
	if err != nil {
		t.Fatal(err)
	}
	before := w.Root()
	rootB, _ := ResolveRoot(b)

	release := w.Hold()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := w.Switch(ctx, Settings{Root: rootB}, nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("Switch while held: err = %v, want ErrBusy", err)
	}
	if w.Root() != before {
		t.Fatal("root changed although the switch failed")
	}

	// Once released the switch goes through.
	done := make(chan error, 1)
	go func() {
		done <- w.Switch(context.Background(), Settings{Root: rootB}, nil)
	}()
	time.Sleep(50 * time.Millisecond)
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Switch after release: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Switch did not finish after release")
	}
	if w.Root() != rootB {
		t.Errorf("Root = %q, want %q", w.Root(), rootB)
	}
}

func TestSwitchDuringErrorKeepsWorkspace(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	w, _ := New(a, 0)
	before := w.Root()
	rootB, _ := ResolveRoot(b)
	boom := errors.New("boom")
	if err := w.Switch(context.Background(), Settings{Root: rootB}, func() error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if w.Root() != before {
		t.Error("root changed although during failed")
	}
	if err := w.Switch(context.Background(), Settings{Root: "relative"}, nil); err == nil {
		t.Error("relative root accepted")
	}
}

func TestResolveRoot(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveRoot(dir); err != nil {
		t.Errorf("existing dir: %v", err)
	}
	for name, in := range map[string]string{
		"empty":    "  ",
		"relative": "some/dir",
		"missing":  filepath.Join(dir, "nope"),
		"file":     file,
		"fs root":  "/",
	} {
		if _, err := ResolveRoot(in); err == nil {
			t.Errorf("%s (%q): accepted", name, in)
		}
	}
	home, err := os.UserHomeDir()
	if err == nil {
		if got, err := ResolveRoot("~"); err == nil {
			want, _ := filepath.EvalSymlinks(home)
			if got != want {
				t.Errorf("ResolveRoot(~) = %q, want %q", got, want)
			}
		}
	}
}
