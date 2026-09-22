package schemata

import (
	"math"
	"testing"
	"time"
)

func TestComparisons(t *testing.T) {
	if !Less(1, 2) || Less(2, 2) {
		t.Error("Less")
	}
	if !LessEq(2, 2) || LessEq(3, 2) || LessEq(math.NaN(), 1) {
		t.Error("LessEq")
	}
	if !Greater("b", "a") || Greater("a", "a") {
		t.Error("Greater")
	}
	if !GreaterEq(0) || GreaterEq(-1) {
		t.Error("GreaterEq")
	}
	if !ConstLeft(11) || ConstLeft(10) {
		t.Error("ConstLeft")
	}
	x := 1
	if !IsNil(nil) || IsNil(&x) {
		t.Error("IsNil")
	}
	if NotNil(nil) || !NotNil([]int{}) {
		t.Error("NotNil")
	}
	if !IsTarget(ErrTarget) || IsTarget(nil) {
		t.Error("IsTarget")
	}
	if !SameInterface(1, 1) || SameInterface(1, "1") {
		t.Error("SameInterface")
	}
}

func TestArithmetic(t *testing.T) {
	if Add(2, 3) != 5 {
		t.Error("Add")
	}
	if Sub(2, 0.5) != 1.5 {
		t.Error("Sub")
	}
	if Scale(time.Second) != 2*time.Second {
		t.Error("Scale")
	}
	if Div(7, 2) != 3 {
		t.Error("Div")
	}
	if Mod(7, 2) != 1 || Mod(8, 3) != 2 {
		t.Error("Mod")
	}
	if MulComplex(1i, 1i) != -1 {
		t.Error("MulComplex")
	}
	if Half(3) != 1.5 {
		t.Error("Half")
	}
	if Sum(1, 2) != 3 || Sum(0.5, 0.25) != 0.75 {
		t.Error("Sum")
	}
	if Shift(3) != 9 {
		t.Error("Shift")
	}
}

func TestLogical(t *testing.T) {
	calls := 0
	count := func() bool { calls++; return true }
	if And(false, count) || calls != 0 {
		t.Error("And must short-circuit")
	}
	if !And(true, count) || calls != 1 {
		t.Error("And")
	}
	if !Or(true, count) || calls != 1 {
		t.Error("Or must short-circuit")
	}
	if !Or(false, count) || calls != 2 {
		t.Error("Or")
	}
	if !Between(1, 2, 3) || Between(2, 2, 3) || Between(1, 3, 3) || !Between(5, 0, 5) || Between(1, 3, 2) {
		t.Error("Between")
	}
}

func TestBitwise(t *testing.T) {
	if Mask(6, 3) != 2 || Either(6, 3) != 7 {
		t.Error("Mask/Either")
	}
	if Shl(1, 3) != 8 {
		t.Error("Shl")
	}
	if Negate(2) != -2 {
		t.Error("Negate")
	}
}

func TestCalls(t *testing.T) {
	var log []string
	Twice(&log)
	if len(log) != 2 || log[0] != "a" || log[1] != "b" {
		t.Errorf("Twice: %v", log)
	}
	log = nil
	if SkipInitCall(&log) != 2 || len(log) != 3 || log[0] != "init" || log[1] != "post" {
		t.Errorf("SkipInitCall: %v", log)
	}
	if SkipNamedBoolCond(true) != 1 || SkipNamedBoolCond(false) != 0 {
		t.Error("SkipNamedBoolCond")
	}
}

func TestConstants(t *testing.T) {
	if Offset(1) != 2 || Quarter(8) != 2 {
		t.Error("Offset/Quarter")
	}
	if SkipNamedConst(1) != 2 || Boxed() != 7 {
		t.Error("SkipNamedConst/Boxed")
	}
	if SkipShiftConst(1) != 5 || NoSiteArrayLen() != 2 || NoSiteArrayKey() != 9 {
		t.Error("SkipShiftConst/NoSite*")
	}
}

func TestStatements(t *testing.T) {
	if Sign(1) != 1 || Sign(0) != -1 || Sign(-1) != -1 {
		t.Error("Sign")
	}
	if CountTo(3) != 3 || CountTo(0) != 0 {
		t.Error("CountTo")
	}
	m := map[string]int{}
	Tally(m, "a")
	Tally(m, "a")
	if m["a"] != 2 {
		t.Error("Tally")
	}
	x := 1
	Decrement(&x)
	if x != 0 {
		t.Error("Decrement")
	}
	var c Counter
	c.Inc()
	if c.n != 1 {
		t.Error("Counter.Inc")
	}
	if Classify(-1) != "negative" || Classify(0) != "non-negative" {
		t.Error("Classify")
	}
	if v, err := Pair(4); v != 4 || err != nil {
		t.Error("Pair")
	}
	if v, err := Halve(4); v != 2 || err != nil {
		t.Error("Halve")
	}
	if v, err := Halve(3); v != 0 || err == nil {
		t.Error("Halve of an odd number")
	}
	if !IsEmpty(nil) || IsEmpty([]int{1}) {
		t.Error("IsEmpty")
	}
	if !NotEmpty([]int{1}) || NotEmpty(nil) {
		t.Error("NotEmpty")
	}
}

func TestLoops(t *testing.T) {
	if FirstEven([]int{1, 2}) != 2 || FirstEven([]int{1}) != -1 {
		t.Error("FirstEven")
	}
	if CountUntil([]int{1, 2, 3}, 2) != 1 || CountUntil([]int{1, 2, 3}, 9) != 3 {
		t.Error("CountUntil")
	}
}

func TestBranches(t *testing.T) {
	if Bound(0, 5) != 5 || Bound(7, 5) != 8 || Bound(5, 5) != 6 {
		t.Error("Bound")
	}
	if Describe(-1) != "negative" || Describe(1) != "positive" || Describe(0) != "zero!" {
		t.Error("Describe")
	}
	ch := make(chan int, 2)
	ch <- 1
	ch <- 2
	if Drain(ch) != 2 {
		t.Error("Drain")
	}
}

func TestLibrary(t *testing.T) {
	if !Prefixed("gopher") || Prefixed("cargo") {
		t.Error("Prefixed")
	}
	if Smallest([]int{3, 1, 2}) != 1 || Smallest([]int{5, 4}) != 4 {
		t.Error("Smallest")
	}
	c := NewConfig([]string{"b"})
	if len(c.Names) != 2 || c.Names[0] != "a" || c.Names[1] != "b" || !c.Verbose || c.Retries != 6 {
		t.Errorf("NewConfig: %+v", c)
	}
}

func TestSkipped(t *testing.T) {
	if SkipConst() != 6 {
		t.Error("SkipConst")
	}
	if !SkipNamedBool(1, 2) || SkipNamedBool(2, 2) {
		t.Error("SkipNamedBool")
	}
	if !SkipNamedBoolOperands(true, true) || SkipNamedBoolOperands(true, false) {
		t.Error("SkipNamedBoolOperands")
	}
	if !SkipRecover(func() { panic("boom") }) || SkipRecover(func() {}) {
		t.Error("SkipRecover")
	}
	m := map[int]int{}
	SkipMapPost(m, 0)
	if m[0] != 3 {
		t.Error("SkipMapPost")
	}
}

// TestLessRedundant repeats part of TestComparisons, so the minimizer
// reports it as subsumed.
func TestLessRedundant(t *testing.T) {
	if !Less(1, 2) || Less(2, 2) {
		t.Error("Less")
	}
}

// TestRegression_Sign repeats part of TestStatements but is kept by name.
func TestRegression_Sign(t *testing.T) {
	if Sign(1) != 1 || Sign(0) != -1 {
		t.Error("Sign")
	}
}

// TestTagged repeats part of TestArithmetic but is kept by its tag.
//
//mutrim:keep
func TestTagged(t *testing.T) {
	if Add(2, 3) != 5 {
		t.Error("Add")
	}
}
