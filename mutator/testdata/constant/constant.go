package constant

import "time"

type Level uint8

const Limit = 10 // outside a body: never a site

func Inc(x int) int             { return x + 1 }
func Big(x int64) int64         { return x + 0xff }
func Frac(x float64) float64    { return x*0.5 + 1e3 }
func Byte(b byte) byte          { return b + 1 }
func Named(l Level) Level       { return l + 1 }
func Untyped() any              { return 7 }
func Wait(d time.Duration) bool { return d > 2*time.Second }
func Index(xs []int) int        { return xs[0] }
func Shifted(n uint) int64      { return 1 << n }
func ConstShift() int           { return 1 << 2 }
func Overflow(b int8) bool      { return b == 127 }
func ArrayLen() int             { var a [4]int; return len(a) }
func ArrayKey() int             { return [...]int{2: 5}[2] }
func MapKey(m map[int]int) int  { return map[int]int{2: 5}[2] + m[1] }
func LocalConst() int           { const c = 3; return c }
func Case(x int) bool {
	switch x {
	case 1:
		return true
	}
	return false
}
