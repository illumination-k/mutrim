package killable

import "testing"

func TestIsPositive(t *testing.T) {
	if IsPositive(0) {
		t.Fatal("0 is not positive")
	}
	if !IsPositive(1) {
		t.Fatal("1 is positive")
	}
}
