package workspace

import (
	"sync/atomic"
	"testing"
)

func TestNotifyEventsKeepSeqOrder(t *testing.T) {
	ws, err := New(t.TempDir(), DefaultMaxRead)
	if err != nil {
		t.Fatal(err)
	}
	events, unsubscribe := ws.Subscribe()
	defer unsubscribe()

	entered := make(chan struct{})
	release := make(chan struct{})
	var armed atomic.Bool
	ws.hub.testAfterNotify = func() {
		if armed.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
	}

	firstDone := make(chan struct{})
	go func() {
		ws.hub.notify("write", "a.txt", "")
		close(firstDone)
	}()
	<-entered

	// A second mutation is queued while the first one is paused. The
	// subscriber must still see the lower sequence number first.
	ws.hub.notify("write", "b.txt", "")
	close(release)
	<-firstDone

	first := <-events
	second := <-events
	if first.Seq >= second.Seq {
		t.Fatalf("events out of order: seq %d (%s) then seq %d (%s)",
			first.Seq, first.Path, second.Seq, second.Path)
	}
	if first.Path != "a.txt" || second.Path != "b.txt" {
		t.Fatalf("events out of order: %s then %s", first.Path, second.Path)
	}
}
