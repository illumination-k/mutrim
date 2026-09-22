package stmt

var log []string

func record(s string) { log = append(log, s) }

func count() int { return len(log) }

func Step(x int) int {
	record("step")
	if x > 0 {
		record("pos")
	}
	for record("init"); x < 3; record("post") {
		x++
	}
	count() // not void: no site
	if x == 0 {
		panic("zero") // panic is kept: the function must still terminate
	}
	return x
}

type Flag bool

// Named's condition has a defined bool type: forced through the overlay
// only, since the runtime helper returns a plain bool.
func Named(f Flag) int {
	if f {
		return 1
	}
	return 0
}
