// Package arid exercises the arid rules. Every line whose mutants the
// built-in rules make arid is marked "// arid", which is what the test
// compares against; nothing else is ignored. A line marked
// "// arid: text" holds arid mutants only where the description contains
// text, as when a block made arid by propagation opens on the line of a
// condition that is not.
package arid

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"
)

// The call statement is a voidcall site and the logged expressions hold
// sites of their own; the rule covers the statement and its arguments.
func Retry(n int) int {
	log.Printf("retrying %d times", n+1) // arid
	slog.Info("retrying", "n", n*2)      // arid
	return n + 1
}

// A method of a logger is covered by the same rule, through the type of
// its receiver rather than the name the receiver is spelled with.
func Report(l *log.Logger, s *slog.Logger, n int) {
	l.Printf("n is %d", n-1)     // arid
	s.Debug("n", "value", n/2)   // arid
	s.With("n", n+1).Info("hey") // arid
}

// Any other call keeps its mutants, arguments included.
func Describe(n int) string {
	return fmt.Sprintf("n is %d", n+1)
}

// Writes to the standard streams, sleeps, sinks and timeouts are arid;
// the call taking the timeout is not.
func Wait(ctx context.Context, n int) (context.Context, context.CancelFunc) {
	fmt.Println("waiting", n+1)                       // arid
	fmt.Fprintf(os.Stderr, "waiting %d\n", n+2)       // arid
	time.Sleep(time.Duration(n+3) * time.Millisecond) // arid
	_ = n + 4                                         // arid
	return context.WithTimeout(ctx,
		time.Duration(n)*time.Second) // arid
}

var cache = map[int]int{}

// The map-cache lookup is arid: skipping it only recomputes the value.
func Square(n int) int {
	if v, ok := cache[n]; ok { // arid
		return v // arid
	} // arid
	v := n * n
	cache[n] = v
	return v
}

// A block whose statements are all arid is arid too, so emptying it is
// ignored, while the condition around it keeps its mutants.
func Check(n int) bool {
	if n > 10 { // arid: if body
		log.Print("large") // arid
		slog.Info("large") // arid
	}
	return n > 5
}

// Mode switches between two values; the other case is unreachable.
func Mode(fast bool) int {
	if fast {
		return 1
	}
	if !fast {
		return 2
	}
	panic("unreachable") // arid
}

type T struct{ n int }

// A formatting method has no behaviour a test asserts on.
func (t T) String() string {
	return fmt.Sprint(t.n + 1) // arid
}
