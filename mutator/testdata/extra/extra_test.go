package extra

import "testing"

func TestClamp(t *testing.T) {
	if Clamp(5, 0, 3) != 3 || Clamp(-1, 0, 3) != 0 || Clamp(2, 0, 3) != 2 {
		t.Fatal("Clamp")
	}
}

func TestWords(t *testing.T) {
	if Words("a b") != 2 {
		t.Fatal("Words")
	}
}
