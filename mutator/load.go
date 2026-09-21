package mutator

import (
	"errors"
	"fmt"

	"golang.org/x/tools/go/packages"
)

// LoadMode is the go/packages mode required by Generate.
const LoadMode = packages.NeedName |
	packages.NeedFiles |
	packages.NeedTypes |
	packages.NeedTypesInfo |
	packages.NeedSyntax

// Load loads the packages matching patterns relative to dir.
// It fails if any package has errors, since mutants of a broken package
// cannot be type-checked meaningfully.
func Load(dir string, patterns ...string) ([]*packages.Package, error) {
	cfg := &packages.Config{Mode: LoadMode, Dir: dir}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, err
	}
	var errs []error
	for _, p := range pkgs {
		for _, e := range p.Errors {
			errs = append(errs, fmt.Errorf("%s: %w", p.PkgPath, e))
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("mutator: no packages matched %v", patterns)
	}
	return pkgs, nil
}
