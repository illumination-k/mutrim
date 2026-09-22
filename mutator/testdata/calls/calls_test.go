package calls

import (
	"testing"
	"time"
)

func TestCalls(t *testing.T) {
	if !Prefixed("gopher") || Prefixed("cargo") {
		t.Error("Prefixed")
	}
	if Lower("Go") != "go" {
		t.Error("Lower")
	}
	if Smallest([]int{3, 1, 2}) != 1 {
		t.Error("Smallest")
	}
	now := time.Now()
	if !Earlier(now, now.Add(time.Second)) || Earlier(now, now) {
		t.Error("Earlier")
	}
	if !EarlierCall(func() time.Time { return now }, now.Add(time.Second)) {
		t.Error("EarlierCall")
	}
	if Count("ab") != 3 {
		t.Error("Count")
	}
	if got := Widths([]string{"a", "bc"}); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Errorf("Widths: %v", got)
	}
	if k, v, ok := Split("a=b"); k != "a" || v != "b" || !ok {
		t.Error("Split")
	}
	if Deferred("ab") != 2 {
		t.Error("Deferred")
	}
	if Recovered() != 1 {
		t.Error("Recovered")
	}
}
