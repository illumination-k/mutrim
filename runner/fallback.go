package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"

	"github.com/illumination-k/mutrim/mutator"
)

// buildFallback builds into dir, with `go test -c`, the test binary of
// every one of Options.bins some row of byBin (its rows, by binary)
// reaching mutant m runs in, from the package's sources with m's Fallback
// overlay: the original code with m alone applied, which the schemata
// could not express. It returns Options.bins with those binaries swapped
// in, or nil when a build fails, which is how a mutant the local type
// check let through fails to compile (an unused variable, a constant
// overflow). A missing go command or a canceled run is an error.
func (o Options) buildFallback(ctx context.Context, m mutator.Mutant, byBin [][]string, dir string) ([]Binary, error) {
	overlay, err := filepath.Abs(m.Fallback)
	if err != nil {
		return nil, err
	}
	bins := slices.Clone(o.bins)
	for i := range bins {
		if len(byBin[i]) == 0 {
			continue
		}
		pkg := bins[i].Pkg
		if i == 0 {
			pkg = m.Pkg
		}
		bins[i].Path = filepath.Join(dir, fmt.Sprintf("%d.test", i))
		args := append([]string{"test", "-c", "-overlay", overlay, "-o", bins[i].Path}, o.BuildFlags...)
		cmd := exec.CommandContext(ctx, "go", append(args, pkg)...) //nolint:gosec // building the user's package is the point
		cmd.Dir = bins[i].Dir
		out, err := cmd.CombinedOutput()
		var exitErr *exec.ExitError
		switch {
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case errors.As(err, &exitErr):
			o.logger.Printf("%s build of %s failed: %s", m.ID, pkg, firstError(out))
			return nil, nil
		case err != nil:
			return nil, fmt.Errorf("runner: build fallback of %s: %w", m.ID, err)
		}
	}
	return bins, nil
}

// firstError is the first line of go build output that is no "# pkg"
// header.
func firstError(out []byte) []byte {
	for line := range bytes.Lines(out) {
		if !bytes.HasPrefix(line, []byte("#")) {
			return bytes.TrimSpace(line)
		}
	}
	return nil
}
