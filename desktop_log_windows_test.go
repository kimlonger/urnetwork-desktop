package main

import "testing"

func TestNewestLogFirstKeepsContinuationLines(t *testing.T) {
	input := "truncated line\n2026/11/15 01:00:00 older\r\nfirst detail\nI1115 01:00:01 newer\nsecond detail\n"
	want := "I1115 01:00:01 newer\r\nsecond detail\r\n2026/11/15 01:00:00 older\r\nfirst detail"
	if got := newestLogFirst(input, true); got != want {
		t.Fatalf("log view = %q, want %q", got, want)
	}
}
