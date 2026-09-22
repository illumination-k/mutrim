// Package branch exercises the branch operator.
package branch

// The body of an if, of an else, and of each case is emptied; an empty body
// is no site.
func Sign(x int) string {
	out := ""
	if x < 0 {
		out = "negative"
	} else {
		out = "positive"
	}
	if x == 0 {
	}
	switch {
	case x == 0:
		out = "zero"
	default:
		out += "!"
	}
	return out
}

// A body whose last statement makes the enclosing statement terminating has
// no schemata form: hiding it would leave the function without a return.
func Terminating(x int) string {
	if x < 0 {
		return "negative"
	} else {
		return "positive"
	}
}

func Fallthrough(x int) string {
	switch x {
	case 0:
		fallthrough
	case 1:
		return "small"
	}
	return "large"
}

// A select clause is emptied like a case clause.
func Recv(ch chan int) int {
	n := 0
	select {
	case v := <-ch:
		n = v
	default:
		n = -1
	}
	return n
}
