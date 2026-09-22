package calc

import "testing"

func TestAbs(t *testing.T) {
	if Abs(-3) != 3 || Abs(3) != 3 || Abs(0) != 0 {
		t.Error("Abs")
	}
}

// TestClamp is table-driven: mutant_calc runs with subtests, so each case
// is a row of the kill matrix and minimize.json judges the cases, not the
// table.
func TestClamp(t *testing.T) {
	cases := []struct {
		name    string
		v, want int
	}{
		{"inside", 5, 5},
		{"below", -1, 1},
		{"above", 11, 10},
		{"low bound", 1, 1},
		{"high bound", 10, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Clamp(tc.v, 1, 10); got != tc.want {
				t.Errorf("Clamp(%d, 1, 10) = %d, want %d", tc.v, got, tc.want)
			}
		})
	}
}

func TestSum(t *testing.T) {
	if Sum() != 0 || Sum(1, 2, 3) != 6 {
		t.Error("Sum")
	}
}

// TestAbsNegative repeats part of TestAbs, so minimize.json reports it as
// subsumed by TestAbs.
func TestAbsNegative(t *testing.T) {
	if Abs(-3) != 3 {
		t.Error("Abs")
	}
}

// TestRegression_Clamp repeats part of TestClamp but is kept by name.
func TestRegression_Clamp(t *testing.T) {
	if Clamp(11, 1, 10) != 10 {
		t.Error("Clamp")
	}
}
