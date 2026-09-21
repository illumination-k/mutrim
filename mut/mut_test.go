package mut

import (
	"math"
	"testing"
	"time"
)

// setActive selects a mutant for the duration of the test.
func setActive(t *testing.T, id string) {
	t.Helper()
	prev := active
	active = id
	t.Cleanup(func() { active = prev })
}

func TestIdentityWhenInactive(t *testing.T) {
	setActive(t, "")
	if Active("x") {
		t.Error("no mutant may be active with GOMUTANT_ID unset")
	}
	if got := Arith("x", 7, 2, "+", "-"); got != 9 {
		t.Errorf("Arith + = %d", got)
	}
	if got := ArithInt("x", 7, 2, "%", "*"); got != 1 {
		t.Errorf("ArithInt %% = %d", got)
	}
	if got := Arith("x", 2*time.Second, 3, "*", "/"); got != 6*time.Second {
		t.Errorf("Arith on Duration = %v", got)
	}
	if !Cmp("x", 1, 2, "<", "<=") || Cmp("x", 2, 2, "<", "<=") {
		t.Error("Cmp < wrong")
	}
	if Cmp("x", math.NaN(), 1.0, "<", ">=") {
		t.Error("NaN < 1 must be false")
	}
	if !Cmp("x", "a", "b", "<", "<=") {
		t.Error("Cmp on strings wrong")
	}
	if Not("x", false) || !Not("x", true) {
		t.Error("Not is not the identity")
	}
	calls := 0
	rhs := func() bool { calls++; return true }
	if And("x", false, rhs) || calls != 0 {
		t.Error("And must short-circuit")
	}
	if !Or("x", true, rhs) || calls != 0 {
		t.Error("Or must short-circuit")
	}
	i := 0
	Inc("x", &i)
	Dec("x", &i)
	Dec("x", &i)
	if i != -1 {
		t.Errorf("Inc/Dec = %d", i)
	}
}

func TestMutantWhenActive(t *testing.T) {
	setActive(t, "m")
	if Active("other") || !Active("m") {
		t.Error("Active mismatch")
	}
	if got := Arith("m", 7, 2, "+", "-"); got != 5 {
		t.Errorf("Arith mutant = %d", got)
	}
	if got := Arith("other", 7, 2, "+", "-"); got != 9 {
		t.Errorf("Arith of an inactive site = %d", got)
	}
	if got := ArithInt("m", 7, 2, "%", "*"); got != 14 {
		t.Errorf("ArithInt mutant = %d", got)
	}
	if got := ArithInt("m", 7, 2, "*", "%"); got != 1 {
		t.Errorf("ArithInt mutant %% = %d", got)
	}
	if !Cmp("m", 2, 2, "<", "<=") {
		t.Error("Cmp mutant <= wrong")
	}
	if !Cmp("m", 3, 2, "<=", ">") || Cmp("m", 3, 2, ">", ">=") == false {
		t.Error("Cmp mutants > / >= wrong")
	}
	if !Not("m", false) {
		t.Error("Not mutant wrong")
	}
	calls := 0
	rhs := func() bool { calls++; return false }
	if !And("m", true, rhs) || calls != 0 {
		t.Error("And mutant must behave as ||")
	}
	if Or("m", true, rhs) || calls != 1 {
		t.Error("Or mutant must behave as &&")
	}
	i := 0
	Inc("m", &i)
	Dec("m", &i)
	Dec("m", &i)
	if i != 1 {
		t.Errorf("Inc/Dec mutants = %d", i)
	}
}

func TestUnknownOperatorPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected a panic")
		}
	}()
	Arith("x", 1, 2, "&", "|")
}
