// Package loop exercises the loopctrl and loopcond operators.
package loop

// break and continue are each other's mutant inside a loop body.
func Scan(xs []int) int {
	n := 0
	for _, x := range xs {
		if x == 0 {
			continue
		}
		if x < 0 {
			break
		}
		n++
	}
	return n
}

// A break that leaves a switch, and a labeled one, are not sites: they do
// not leave the loop.
func InSwitch(xs []int) int {
	n := 0
	for _, x := range xs {
		switch {
		case x == 0:
			break
		default:
			n++
		}
	}
	return n
}

func Labeled(xs [][]int) int {
	n := 0
outer:
	for _, row := range xs {
		for _, x := range row {
			if x < 0 {
				break outer
			}
			n++
		}
	}
	return n
}

// A for condition is forced to false, so the loop runs no iteration; a for
// without a condition has none to force.
func Count(n int) int {
	c := 0
	for i := 0; i < n; i++ {
		c++
	}
	return c
}

func Forever(stop func() bool) int {
	n := 0
	for {
		if stop() {
			return n
		}
		n++
	}
}

// continue -> break is not a site in an unconditional for that no break
// leaves: the loop is the terminating statement of the function.
func Retry(next func() (int, bool)) int {
	for {
		v, ok := next()
		if !ok {
			return 0
		}
		if v < 0 {
			continue
		}
		if v > 10 {
			return v
		}
	}
}

// It is one where a conditional for, or a break, already needs the return.
func RetryBreaks(next func() (int, bool)) int {
	for {
		v, ok := next()
		if !ok {
			break
		}
		if v < 0 {
			continue
		}
	}
	return 0
}

func RetryLabeled(next func() (int, bool)) int {
outer:
	for {
		for {
			v, ok := next()
			if !ok {
				break outer
			}
			if v < 0 {
				continue outer
			}
			break
		}
		if _, ok := next(); ok {
			continue
		}
	}
	return 0
}

// A range loop breaks before its first iteration instead.
func Sum(xs []int) int {
	total := 0
	for _, x := range xs {
		total += x
	}
	return total
}
