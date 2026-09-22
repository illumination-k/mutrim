package report

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Sources resolves the text of the files a mutant names. Paths in
// mutants.json are the ones the generator saw: absolute under `go list`,
// relative to the execution root under Bazel. Neither is guaranteed to
// resolve from the working directory of the reporting run, so the files
// given to NewSources are also indexed by base name and matched on the
// longest shared path suffix.
type Sources struct {
	byBase map[string][]string
}

// NewSources indexes the Go files among paths; a directory contributes
// every .go file under it.
func NewSources(paths []string) (*Sources, error) {
	s := &Sources{byBase: map[string][]string{}}
	for _, p := range paths {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			s.add(p)
			continue
		}
		err = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.HasSuffix(path, ".go") {
				s.add(path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Sources) add(path string) {
	base := filepath.Base(path)
	s.byBase[base] = append(s.byBase[base], path)
}

// Read returns the text of file, or "" when it cannot be found: the
// report is still useful without the source, only the rendered view of
// that file is.
func (s *Sources) Read(file string) string {
	if data, err := os.ReadFile(filepath.Clean(file)); err == nil {
		return string(data)
	}
	if s == nil {
		return ""
	}
	best, bestScore := "", -1
	for _, cand := range s.byBase[filepath.Base(file)] {
		if n := sharedSuffix(file, cand); n > bestScore {
			best, bestScore = cand, n
		}
	}
	if best == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Clean(best))
	if err != nil {
		return ""
	}
	return string(data)
}

// sharedSuffix counts the trailing path elements a and b have in common.
func sharedSuffix(a, b string) int {
	as := strings.Split(filepath.ToSlash(a), "/")
	bs := strings.Split(filepath.ToSlash(b), "/")
	n := 0
	for n < len(as) && n < len(bs) && as[len(as)-1-n] == bs[len(bs)-1-n] {
		n++
	}
	return n
}
