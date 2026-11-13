package tui

import "testing"

func TestLogBuffer(t *testing.T) {
	b := NewLogBuffer(3)
	if _, err := b.Write([]byte("one\ntwo\n")); err != nil {
		t.Fatal(err)
	}
	b.Add("three")
	b.Add("four") // exceeds max, oldest dropped

	got := b.Tail(10)
	want := []string{"two", "three", "four"}
	if len(got) != len(want) {
		t.Fatalf("Tail = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Tail = %v, want %v", got, want)
		}
	}

	if n := len(b.Tail(2)); n != 2 {
		t.Fatalf("Tail(2) returned %d lines", n)
	}

	b.Clear()
	if got := b.Tail(5); len(got) != 0 {
		t.Fatalf("Tail after Clear = %v", got)
	}
}
