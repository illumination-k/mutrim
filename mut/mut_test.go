package mut

import (
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	if got := Arith("x", 7.0, 2.0, "/", "*"); got != 3.5 {
		t.Errorf("Arith / = %v", got)
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
	if got := Bit("x", 6, 3, "&", "|"); got != 2 {
		t.Errorf("Bit & = %d", got)
	}
	if got := Bit("x", 6, 3, "&^", "&"); got != 4 {
		t.Errorf("Bit &^ = %d", got)
	}
	if got := Shift("x", int64(1), uint(3), "<<", ">>"); got != 8 {
		t.Errorf("Shift << = %d", got)
	}
	if got := Neg("x", 2.5); got != -2.5 {
		t.Errorf("Neg = %v", got)
	}
	if !Cond("x", true, false) || Cond("x", false, true) {
		t.Error("Cond is not the identity")
	}
	if got := Const("x", int8(5), 6); got != 5 {
		t.Errorf("Const = %d", got)
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
	if got := Bit("m", 6, 3, "&", "|"); got != 7 {
		t.Errorf("Bit mutant = %d", got)
	}
	if got := Bit("m", 6, 3, "|", "^"); got != 5 {
		t.Errorf("Bit mutant ^ = %d", got)
	}
	if got := Shift("m", uint8(8), 1, "<<", ">>"); got != 4 {
		t.Errorf("Shift mutant = %d", got)
	}
	if got := Neg("m", 2); got != 2 {
		t.Errorf("Neg mutant = %d", got)
	}
	if Cond("m", true, false) || !Cond("m", false, true) {
		t.Error("Cond mutant must return the forced value")
	}
	if got := Const("m", 0.5, 1.5); got != 1.5 {
		t.Errorf("Const mutant = %v", got)
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
	for name, call := range map[string]func(){
		"arith": func() { Arith("x", 1, 2, "&", "|") },
		"cmp":   func() { Cmp("x", 1, 2, "==", "!=") },
		"bit":   func() { Bit("x", 1, 2, "+", "-") },
		"shift": func() { Shift("x", 1, 2, "&", "|") },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("expected a panic")
				}
			}()
			call()
		})
	}
}

// A trace file that cannot be opened is a runner bug, not something to
// silently ignore: the process must not start.
// Reach without GOMUTANT_TRACE does nothing, so schemata sources behave
// like the original.
func TestReachUntraced(t *testing.T) {
	prev := trace
	trace = nil
	t.Cleanup(func() { trace = prev })
	Reach("a")
}

func TestTraceUnopenablePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected a panic")
		}
	}()
	newTracer(filepath.Join(t.TempDir(), "missing", "trace"))
}

// With GOMUTANT_TRACE set, every reached site is written once, whichever
// helper reaches it and whether or not its mutant is active; a block Reach
// records is traced alike.
func TestTraceRecordsReachedSites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace")
	prev := trace
	trace = newTracer(path)
	t.Cleanup(func() { trace = prev })
	setActive(t, "b")

	Active("a")
	Cmp("b", 1, 2, "<", "<=")
	Arith("c", 1, 2, "+", "-")
	Active("a")
	Not("d", true)
	Reach("e")
	Reach("a")

	data, err := os.ReadFile(path) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Fields(string(data)), []string{"a", "b", "c", "d", "e"}; !slices.Equal(got, want) {
		t.Errorf("trace = %v, want %v", got, want)
	}
	if newTracer("") != nil {
		t.Error("an empty GOMUTANT_TRACE must disable tracing")
	}
}
