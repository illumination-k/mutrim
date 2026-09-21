package calc

import "testing"

func TestAbs(t *testing.T) {
	if Abs(-3) != 3 || Abs(3) != 3 || Abs(0) != 0 {
		t.Error("Abs")
	}
}

func TestClamp(t *testing.T) {
	if Clamp(5, 1, 10) != 5 || Clamp(-1, 1, 10) != 1 || Clamp(11, 1, 10) != 10 {
		t.Error("Clamp")
	}
	if Clamp(1, 1, 10) != 1 || Clamp(10, 1, 10) != 10 {
		t.Error("Clamp bounds")
	}
}

func TestSum(t *testing.T) {
	if Sum() != 0 || Sum(1, 2, 3) != 6 {
		t.Error("Sum")
	}
}
