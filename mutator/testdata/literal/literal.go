// Package literal exercises the string, boolean and composite operators.
package literal

type Mode bool

type Label string

type IDs []int

// String literals are emptied, and an empty one is filled. A constant
// declaration, a struct tag and an array length must stay literal.
func Message(name string) string {
	const prefix = "id-"
	tagged := struct {
		Name string `json:"name"`
	}{Name: name}
	var buf [len("ab")]byte
	empty := ""
	return prefix + tagged.Name + string(buf[:]) + empty
}

// A literal of a defined string type is applied but not lowered: the
// runtime helper is given the literal's type, and only a predeclared one
// can be spelled.
func Named() Label { return "label" }

// Boolean literals are flipped, except in a constant declaration; one of a
// defined bool type is applied but not lowered.
func Flags(on bool) (bool, Mode) {
	const always = true
	if always && on {
		return false, true
	}
	return true, false
}

// Slice and map literals lose their elements. A struct literal is no site,
// and one with an elided type has no form outside its context, so it is
// applied but not lowered.
func Table() map[string][]int {
	return map[string][]int{"a": {1, 2}}
}

func Defined() IDs { return IDs{1, 2} }

func Structs() []struct{ N int } {
	return []struct{ N int }{{N: 1}}
}
