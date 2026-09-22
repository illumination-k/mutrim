// Package logging exercises the exclude-calls rule. Every line whose
// mutants the default list covers is marked "// logged", which is what
// the test compares against; nothing else is excluded.
package logging

import (
	"fmt"
	"log"
	"log/slog"
)

// The call statement is a voidcall site and the logged expressions hold
// sites of their own; the rule covers the statement and its arguments.
func Retry(n int) int {
	log.Printf("retrying %d times", n+1) // logged
	slog.Info("retrying", "n", n*2)      // logged
	return n + 1
}

// A method of a logger is covered by the same rule, through the type of
// its receiver rather than the name the receiver is spelled with.
func Report(l *log.Logger, s *slog.Logger, n int) {
	l.Printf("n is %d", n-1)     // logged
	s.Debug("n", "value", n/2)   // logged
	s.With("n", n+1).Info("hey") // logged
}

// Any other call keeps its mutants, arguments included.
func Describe(n int) string {
	return fmt.Sprintf("n is %d", n+1)
}
