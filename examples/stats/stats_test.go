package stats

import (
	"errors"
	"testing"
)

func TestMean(t *testing.T) {
	if m, err := Mean(1, 2, 3); err != nil || m != 2 {
		t.Errorf("Mean(1, 2, 3) = %v, %v", m, err)
	}
	if _, err := Mean(); !errors.Is(err, ErrEmpty) {
		t.Errorf("Mean() error = %v", err)
	}
}

func TestSpread(t *testing.T) {
	if Spread() != 0 || Spread(4) != 0 || Spread(3, 9, 1) != 8 {
		t.Error("Spread")
	}
}

// TestMeanEmpty repeats part of TestMean but is kept by its tag.
//
//mutrim:keep
func TestMeanEmpty(t *testing.T) {
	if _, err := Mean(); !errors.Is(err, ErrEmpty) {
		t.Errorf("Mean() error = %v", err)
	}
}
