package report

import (
	"fmt"
	"io"
	"strings"

	"github.com/illumination-k/mutrim/mutator"
	"github.com/illumination-k/mutrim/runner"
)

// WriteAnnotations writes one GitHub Actions warning per surviving
// mutant, which the workflow run shows on the diff, with the mutant's own
// diff from mutants.json when it has one. Only LIVED and
// NO_COVERAGE are annotated: they are what a reviewer can act on. A
// positive maxPerLine caps the annotations of one source line, as review
// tools surface one mutant per line (Petrović et al., ICSE-SEIP 2018); 0
// annotates every survivor.
func WriteAnnotations(w io.Writer, mutants []mutator.Mutant, reports []*runner.Report, maxPerLine int) error {
	type fileLine struct {
		file string
		line int
	}
	perLine := map[fileLine]int{}
	for _, s := range Survivors(mutants, reports) {
		if maxPerLine > 0 {
			key := fileLine{s.File, s.Line}
			if perLine[key] >= maxPerLine {
				continue
			}
			perLine[key]++
		}
		msg := fmt.Sprintf("%s: %s %s: %s", s.Status, s.Func, s.Operator, s.Description)
		if s.Diff != "" {
			msg += "\n\n" + s.Diff
		}
		line := fmt.Sprintf("::warning file=%s,line=%d,col=%d,endLine=%d,endColumn=%d::%s\n",
			escapeProperty(s.File), s.Line, s.Col, max(s.EndLine, s.Line), max(s.EndCol, s.Col), escapeData(msg))
		if _, err := io.WriteString(w, line); err != nil {
			return err
		}
	}
	return nil
}

// escapeData and escapeProperty percent-encode the characters that would
// otherwise end a workflow command's message or property list.
var (
	dataEscaper     = strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A")
	propertyEscaper = strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A", ":", "%3A", ",", "%2C")
)

func escapeData(s string) string { return dataEscaper.Replace(s) }

func escapeProperty(s string) string { return propertyEscaper.Replace(s) }
