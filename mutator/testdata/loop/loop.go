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

// A range loop breaks before its first iteration instead.
func Sum(xs []int) int {
	total := 0
	for _, x := range xs {
		total += x
	}
	return total
}
