package mutator

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/gcexportdata"
	"golang.org/x/tools/go/packages"
)

// FilesConfig describes one package to LoadFiles.
type FilesConfig struct {
	// ImportPath of the package.
	ImportPath string
	// Files are the package's Go source files.
	Files []string
	// Importcfg is a file in the format of go build -importcfg:
	// "packagefile <path>=<export data>" and "importmap <path>=<path>"
	// lines. It lists the dependencies' export data.
	Importcfg string
	// Stdlib is a directory of compiled standard-library packages laid out
	// as <Stdlib>/<goos_goarch>/<import path>.a; it is searched for import
	// paths that Importcfg does not list.
	Stdlib string
	// Tags are the build tags in effect. Files excluded by their build
	// constraints are listed in IgnoredFiles and not type-checked.
	Tags []string
}

// LoadFiles type-checks one package from its source files without go list,
// reading dependency types from compiler export data. It is how the Bazel
// rule runs gen: a sandboxed action has neither a module nor a build cache,
// but rules_go has already compiled every dependency.
func LoadFiles(cfg FilesConfig) (*packages.Package, error) {
	if cfg.ImportPath == "" || len(cfg.Files) == 0 {
		return nil, errors.New("mutator: LoadFiles needs an import path and at least one file")
	}
	imp, err := newExportImporter(cfg.Importcfg, cfg.Stdlib)
	if err != nil {
		return nil, err
	}
	bctx := build.Default
	bctx.BuildTags = cfg.Tags

	pkg := &packages.Package{ID: cfg.ImportPath, PkgPath: cfg.ImportPath, Fset: token.NewFileSet()}
	for _, name := range cfg.Files {
		ok, err := bctx.MatchFile(filepath.Dir(name), filepath.Base(name))
		if err != nil {
			return nil, fmt.Errorf("mutator: %s: %w", name, err)
		}
		if !ok {
			pkg.IgnoredFiles = append(pkg.IgnoredFiles, name)
			continue
		}
		f, err := parser.ParseFile(pkg.Fset, name, nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		pkg.GoFiles = append(pkg.GoFiles, name)
		pkg.CompiledGoFiles = append(pkg.CompiledGoFiles, name)
		pkg.Syntax = append(pkg.Syntax, f)
	}
	if len(pkg.Syntax) == 0 {
		return nil, fmt.Errorf("mutator: every file of %s is excluded by build constraints", cfg.ImportPath)
	}

	var errs []error
	conf := types.Config{
		Importer: imp,
		Sizes:    types.SizesFor("gc", build.Default.GOARCH),
		Error:    func(err error) { errs = append(errs, err) },
	}
	pkg.TypesInfo = &types.Info{
		Types:        map[ast.Expr]types.TypeAndValue{},
		Instances:    map[*ast.Ident]types.Instance{},
		Defs:         map[*ast.Ident]types.Object{},
		Uses:         map[*ast.Ident]types.Object{},
		Implicits:    map[ast.Node]types.Object{},
		Selections:   map[*ast.SelectorExpr]*types.Selection{},
		Scopes:       map[ast.Node]*types.Scope{},
		FileVersions: map[*ast.File]string{},
	}
	pkg.Types, _ = conf.Check(cfg.ImportPath, pkg.Fset, pkg.Syntax, pkg.TypesInfo)
	if len(errs) > 0 {
		return nil, fmt.Errorf("mutator: %s: %w", cfg.ImportPath, errors.Join(errs...))
	}
	pkg.Name = pkg.Types.Name()
	return pkg, nil
}

// exportImporter resolves imports from export data files: the ones an
// importcfg names, then the standard library directory.
type exportImporter struct {
	fset      *token.FileSet
	files     map[string]string // import path -> export data file
	importmap map[string]string // import path -> package path
	stdlib    string
	packages  map[string]*types.Package
}

func newExportImporter(importcfg, stdlib string) (*exportImporter, error) {
	imp := &exportImporter{
		fset:      token.NewFileSet(),
		files:     map[string]string{},
		importmap: map[string]string{},
		stdlib:    stdlib,
		packages:  map[string]*types.Package{},
	}
	if importcfg == "" {
		return imp, nil
	}
	data, err := os.ReadFile(filepath.Clean(importcfg))
	if err != nil {
		return nil, err
	}
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		verb, arg, _ := strings.Cut(line, " ")
		from, to, ok := strings.Cut(arg, "=")
		switch {
		case !ok:
			return nil, fmt.Errorf("mutator: %s: malformed line %q", importcfg, line)
		case verb == "packagefile":
			imp.files[from] = to
		case verb == "importmap":
			imp.importmap[from] = to
		default:
			return nil, fmt.Errorf("mutator: %s: unknown directive %q", importcfg, verb)
		}
	}
	return imp, nil
}

func (i *exportImporter) Import(path string) (*types.Package, error) {
	if mapped, ok := i.importmap[path]; ok {
		path = mapped
	}
	if path == "unsafe" {
		return types.Unsafe, nil
	}
	if pkg, ok := i.packages[path]; ok && pkg.Complete() {
		return pkg, nil
	}
	file, err := i.lookup(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Clean(file))
	if err != nil {
		return nil, err
	}
	// Both rules_go's .x files and the standard library's .a files are
	// archives whose __.PKGDEF member holds the export data, which
	// NewReader locates. Its deprecation warns that archive support ends
	// with Go 1.29, so a later x/tools may need that lookup done here.
	r, err := gcexportdata.NewReader(bytes.NewReader(data)) //nolint:staticcheck // see above
	if err != nil {
		return nil, fmt.Errorf("mutator: export data of %s (%s): %w", path, file, err)
	}
	pkg, err := gcexportdata.Read(r, i.fset, i.packages, path)
	if err != nil {
		return nil, fmt.Errorf("mutator: export data of %s (%s): %w", path, file, err)
	}
	return pkg, nil
}

// lookup finds the export data of path: an importcfg entry, else
// <stdlib>/*/<path>.a, so the installsuffix (goos_goarch, plus _race and
// the like) need not be known.
func (i *exportImporter) lookup(path string) (string, error) {
	if file, ok := i.files[path]; ok {
		return file, nil
	}
	if i.stdlib != "" {
		matches, err := filepath.Glob(filepath.Join(i.stdlib, "*", filepath.FromSlash(path)+".a"))
		if err != nil {
			return "", err
		}
		if len(matches) > 0 {
			return matches[0], nil
		}
	}
	return "", fmt.Errorf("mutator: no export data for %q", path)
}
