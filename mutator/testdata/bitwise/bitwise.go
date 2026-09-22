package bitwise

func Mask(a, b uint8) uint8      { return a & b }
func Either(a, b int) int        { return a | b }
func Xor(a, b int) int           { return a ^ b }
func Clear(a, b int) int         { return a &^ b }
func Shl(a int64, n uint) int64  { return a << n }
func Shr(a uint32, n int) uint32 { return a >> n }

// ConstShift's schemata are declined: the helper would type 1 as int.
func ConstShift(n uint) int64 { return 1 << n }

func Neg(x int) int              { return -x }
func NegFloat(x float64) float64 { return -x + 1 }

// NegConst is a constant expression: +5 through the overlay, no schemata.
func NegConst() int { return -5 }
