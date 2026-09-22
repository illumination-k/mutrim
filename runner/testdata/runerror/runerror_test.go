package runerror

import "testing"

// TestDie reaches every site of Die, so its mutant runs against this
// row: the ones that skip the if body fall through to the exit below,
// which dies from outside the tests instead of failing the row.
func TestDie(t *testing.T) {
	if v := Die(1); v != 1 {
		t.Errorf("Die(1) = %d, want 1", v)
	}
}
