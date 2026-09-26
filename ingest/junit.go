package ingest

import (
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
)

// junitSuite is the part of a JUnit XML report the adapter reads: the
// root <testsuites> or a <testsuite>, which either nests.
type junitSuite struct {
	Suites []junitSuite `xml:"testsuite"`
	Cases  []struct {
		Classname string `xml:"classname,attr"`
		Name      string `xml:"name,attr"`
		Time      string `xml:"time,attr"`
	} `xml:"testcase"`
}

// JUnit reads the test durations of a JUnit XML report into rows named
// the way the other adapters of runner's ecosystem name them: "vitest"
// (the test file, "#", the describe blocks and the test joined by spaces,
// as the Stryker adapter reads them) or "nextest" (the crate of the test
// binary, "::", the test path, as the cargo-mutants adapter does).
func JUnit(data []byte, runner string) (*Observations, error) {
	var name func(classname, test string) string
	switch runner {
	case "vitest":
		name = func(classname, test string) string {
			return classname + "#" + strings.ReplaceAll(test, " > ", " ")
		}
	case "nextest":
		name = func(classname, test string) string { return nextestCrate(classname) + "::" + test }
	default:
		return nil, fmt.Errorf("junit: unknown runner %q (vitest, nextest)", runner)
	}
	var root junitSuite
	if err := xml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("junit: %w", err)
	}
	rows := map[string]*Test{}
	var walk func(s junitSuite)
	walk = func(s junitSuite) {
		for _, c := range s.Cases {
			secs, _ := strconv.ParseFloat(c.Time, 64)
			n := name(c.Classname, c.Name)
			if rows[n] == nil {
				rows[n] = &Test{Name: n}
			}
			rows[n].DurationMS = max(rows[n].DurationMS, int64(secs*1000))
		}
		for _, sub := range s.Suites {
			walk(sub)
		}
	}
	walk(root)
	return &Observations{Source: "junit", Tests: sortedRows(rows)}, nil
}
