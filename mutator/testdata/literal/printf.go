package literal

import (
	"errors"
	"fmt"
)

// The format of a printf-like call is reported but ignored: lowered, it
// would no longer be constant, which vet's printf check rejects. A
// print-style argument and a formatted argument are mutated.
func Errors(n int) []error {
	return []error{
		fmt.Errorf("boom"),
		fmt.Errorf(("n=" + "%d"), n),
		fmt.Errorf("%s", "arg"),
		errors.New("plain"),
		logf("wrapped"),
	}
}

func logf(format string, args ...any) error { return fmt.Errorf(format, args...) }
