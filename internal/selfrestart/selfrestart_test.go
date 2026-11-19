package selfrestart

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fake builds a Restarter whose timer and command are recorded instead of
// run, so nothing real is restarted.
func fake(inUnit bool) (*Restarter, *[]func(), *[][]string) {
	var timers []func()
	var calls [][]string
	r := &Restarter{
		unit:   Unit,
		delay:  DefaultDelay,
		inUnit: func() bool { return inUnit },
		run: func(name string, args ...string) error {
			calls = append(calls, append([]string{name}, args...))
			return nil
		},
		after: func(d time.Duration, f func()) {
			if d != DefaultDelay {
				panic("unexpected delay")
			}
			timers = append(timers, f)
		},
	}
	r.logger = New(nil).logger
	return r, &timers, &calls
}

func TestScheduleRestartsAfterDelayWithNoBlock(t *testing.T) {
	r, timers, calls := fake(true)
	msg, err := r.Schedule()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "scheduled") {
		t.Fatalf("message = %q", msg)
	}
	// Nothing runs before the delay, so the reply can go out first.
	if len(*calls) != 0 || len(*timers) != 1 {
		t.Fatalf("calls=%v timers=%d, want no call and one timer", *calls, len(*timers))
	}
	(*timers)[0]()
	want := [][]string{{"systemctl", "--user", "--no-block", "restart", Unit}}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("calls = %v, want %v", *calls, want)
	}
}

func TestScheduleOnlyOnce(t *testing.T) {
	r, timers, _ := fake(true)
	if _, err := r.Schedule(); err != nil {
		t.Fatal(err)
	}
	msg, err := r.Schedule()
	if err != nil || !strings.Contains(msg, "already scheduled") {
		t.Fatalf("second Schedule = %q, %v", msg, err)
	}
	if len(*timers) != 1 {
		t.Fatalf("timers = %d, want 1", len(*timers))
	}
}

func TestScheduleFailureAllowsRetry(t *testing.T) {
	r, timers, _ := fake(true)
	r.run = func(string, ...string) error { return errors.New("no bus") }
	if _, err := r.Schedule(); err != nil {
		t.Fatal(err)
	}
	(*timers)[0]()
	if _, err := r.Schedule(); err != nil {
		t.Fatal(err)
	}
	if len(*timers) != 2 {
		t.Fatalf("a failed restart should allow a new one; timers = %d", len(*timers))
	}
}

func TestScheduleRefusesOutsideService(t *testing.T) {
	r, timers, _ := fake(false)
	if _, err := r.Schedule(); !errors.Is(err, ErrNotService) {
		t.Fatalf("err = %v, want ErrNotService", err)
	}
	if len(*timers) != 0 {
		t.Fatal("restart scheduled outside the service")
	}
}

func TestInUnit(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]bool{
		"0::/user.slice/user-1000.slice/user@1000.service/app.slice/ngodingmcp.service\n":     true,
		"0::/user.slice/user-1000.slice/user@1000.service/app.slice/ngodingmcp.service/sub\n": true,
		"0::/user.slice/user-1000.slice/session-2.scope\n":                                    false,
		"0::/user.slice/user-1000.slice/user@1000.service/app.slice/ngodingmcp.service.bak\n": false,
		"": false,
	}
	i := 0
	for content, want := range cases {
		i++
		p := filepath.Join(dir, "cgroup"+string(rune('a'+i)))
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := InUnit(p, Unit); got != want {
			t.Errorf("InUnit(%q) = %v, want %v", content, got, want)
		}
	}
	if InUnit(filepath.Join(dir, "missing"), Unit) {
		t.Error("missing cgroup file should not count as in unit")
	}
}
