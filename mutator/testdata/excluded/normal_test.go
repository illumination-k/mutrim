package excluded

import "testing"

func TestNormal(t *testing.T) {
	if !Normal(1, 2) {
		t.Fatal("1 < 2")
	}
}
