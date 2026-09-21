package control

func Sum(xs []int) int {
	total := 0
	for i := 0; i < len(xs); i++ {
		if xs[i] > 0 {
			total += xs[i]
		}
	}
	for total > 100 {
		total--
	}
	for {
		break
	}
	return total
}

type Counter struct{ n int }

func (c *Counter) Inc() {
	c.n++
}

func Closure() func() bool {
	return func() bool {
		if true {
			return false
		}
		return true
	}
}
