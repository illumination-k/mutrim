package report

import (
	"fmt"
	"io"
	"strings"

	"github.com/illumination-k/mutrim/mutator"
	"github.com/illumination-k/mutrim/runner"
)

// WriteAnnotations writes one GitHub Actions warning per surviving
// mutant, which the workflow run shows on the diff. Only LIVED and
// NO_COVERAGE are annotated: they are what a reviewer can act on.
func WriteAnnotations(w io.Writer, mutants []mutator.Mutant, reports []*runner.Report) error {
	for _, s := range Survivors(mutants, reports) {
		line := fmt.Sprintf("::warning file=%s,line=%d,col=%d,endLine=%d,endColumn=%d::%s\n",
			escapeProperty(s.File), s.Line, s.Col, max(s.EndLine, s.Line), max(s.EndCol, s.Col),
			escapeData(fmt.Sprintf("%s: %s %s: %s", s.Status, s.Func, s.Operator, s.Description)))
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
