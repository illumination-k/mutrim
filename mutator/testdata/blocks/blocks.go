// Package blocks is the fixture of mutator.Blocks: one block of each kind.
package blocks

func Straight(xs []int) []int {
	xs = append(xs, 1)
	return xs
}

func Every(n int, ch chan int) int {
	if n > 0 {
		n--
	} else {
		n++
	}
	for i := 0; i < n; i++ {
	}
	for range n {
	}
	switch n {
	case 1:
	default:
	}
	select {
	case <-ch:
	default:
	}
	f := func() int { return n }
	{
		n = f()
	}
	return n
}
