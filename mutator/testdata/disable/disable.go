package disable

// Block is covered by a disable block until the matching enable.
func Block(a, b int) int {
	//mutrim:disable relational,constant the caller already range-checks
	if a < 0 {
		return 1
	}
	//mutrim:enable
	if b < 0 {
		return 2
	}
	return a + b
}

// NextLine suppresses one line, and only for one operator.
func NextLine(a, b int) bool {
	//mutrim:disable-next-line relational
	return a <= b && a != 0
}

//mutrim:disable-func equivalent under every input
func Whole(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}

// Reason keeps a reason that starts with a word that is not an operator.
func Reason(a int) int {
	//mutrim:disable-next-line hand-checked
	return a * 2
}

// Unknown directives are not ours; this function is mutated normally.
//
//mutrim:keep
func Unknown(a, b int) bool {
	return a == b
}

// Tail opens a disable that nothing closes, so it runs to the end of the file.
func Tail(a, b int) bool {
	//mutrim:disable
	return a >= b
}
