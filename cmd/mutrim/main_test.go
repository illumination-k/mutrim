package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/illumination-k/mutrim/mutator"
)

const fixture = "../../mutator/testdata/killable"

func TestGenThenOverlay(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"gen", fixture}, &stdout, &stderr); err != nil {
		t.Fatalf("gen: %v\n%s", err, stderr.String())
	}
	var mutants []mutator.Mutant
	if err := json.Unmarshal(stdout.Bytes(), &mutants); err != nil {
		t.Fatalf("gen output is not JSON: %v\n%s", err, stdout.String())
	}
	if len(mutants) == 0 {
		t.Fatal("gen produced no mutants")
	}

	dir := t.TempDir()
	stdout.Reset()
	if err := run([]string{"overlay", "-id", mutants[0].ID, "-dir", dir, fixture}, &stdout, &stderr); err != nil {
		t.Fatalf("overlay: %v\n%s", err, stderr.String())
	}
	var overlay mutator.Overlay
	if err := json.Unmarshal(stdout.Bytes(), &overlay); err != nil {
		t.Fatalf("overlay output is not JSON: %v\n%s", err, stdout.String())
	}
	if len(overlay.Replace) != 1 {
		t.Fatalf("want one replacement, got %v", overlay.Replace)
	}
	for orig, mutated := range overlay.Replace {
		if filepath.Base(orig) != "killable.go" || filepath.Dir(mutated) != dir {
			t.Errorf("unexpected replacement %s -> %s", orig, mutated)
		}
		if _, err := os.Stat(mutated); err != nil {
			t.Error(err)
		}
	}
}

func TestUnknownCommand(t *testing.T) {
	var stderr bytes.Buffer
	err := run([]string{"bogus"}, &bytes.Buffer{}, &stderr)
	if err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("want usage error, got %v", err)
	}
}
