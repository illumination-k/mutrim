package equivalent

import "testing"

func TestScale(t *testing.T) {
	if got := Scale(3); got != 3 {
		t.Errorf("Scale(3) = %d, want 3", got)
	}
}

func TestMax(t *testing.T) {
	if got := Max(1, 2); got != 2 {
		t.Errorf("Max(1, 2) = %d, want 2", got)
	}
	if got := Max(2, 1); got != 2 {
		t.Errorf("Max(2, 1) = %d, want 2", got)
	}
}

func TestRecord(t *testing.T) {
	Record(1)
}
