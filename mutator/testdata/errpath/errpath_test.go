package errpath

import (
	"errors"
	"testing"
)

func TestParse(t *testing.T) {
	if n, err := Parse("12"); n != 12 || err != nil {
		t.Errorf("Parse(12) = %d, %v", n, err)
	}
	_, err := Parse("")
	if !IsEmpty(err) || err.Error() != `parse "": empty` {
		t.Errorf("Parse() = %v", err)
	}
	_, err = Parse("x")
	if NumError(err) == nil || err.Error() == NumError(err).Error() {
		t.Errorf("Parse(x) = %v", err)
	}
	if IsEmpty(err) {
		t.Errorf("IsEmpty(%v)", err)
	}
	if NumError(errors.New("other")) != nil {
		t.Error("NumError of an unrelated error")
	}
}

func TestPanics(t *testing.T) {
	Must(1)
	if Check(2) != 2 || Abs(3) != 3 {
		t.Error("Check or Abs")
	}
	if err := Safe(func() { Must(-1) }); err == nil || err.Error() != "panic: negative" {
		t.Errorf("Safe(Must(-1)) = %v", err)
	}
	if err := Safe(func() { Check(-1) }); err == nil {
		t.Error("Check(-1) must panic")
	}
	if err := Safe(func() {}); err != nil {
		t.Errorf("Safe() = %v", err)
	}
	Discard(func() { panic("x") })
}
