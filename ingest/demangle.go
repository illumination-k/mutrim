package ingest

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// Demangle returns the path of a Rust symbol ("crate::tests::name"), as
// far as matching a function by path needs: the legacy mangling, and the
// v0 one's crate roots, nested paths, closures and backrefs. A generic or
// an impl path, which a v0 symbol spells with types, and a name that is
// no Rust symbol are returned as they are. llvm-cov prefixes a function
// of internal linkage with its file ("src/lib.rs:_RNv..."); the prefix is
// dropped.
func Demangle(name string) string {
	sym := name
	if i := strings.LastIndexByte(sym, ':'); i >= 0 {
		sym = sym[i+1:]
	}
	var path string
	var err error
	switch {
	case strings.HasPrefix(sym, "_R"):
		path, err = demangleV0(sym[len("_R"):])
	case strings.HasPrefix(sym, "_ZN"):
		path, err = demangleLegacy(sym[len("_ZN"):])
	default:
		return name
	}
	if err != nil {
		return name
	}
	return path
}

var errMangled = errors.New("unsupported mangling")

// legacyHash is the hash segment ending a legacy symbol.
var legacyHash = regexp.MustCompile(`^h[0-9a-f]{16}$`)

// demangleLegacy reads the length-prefixed segments up to the closing E,
// leaving the $-escapes as they are.
func demangleLegacy(s string) (string, error) {
	var segs []string
	for !strings.HasPrefix(s, "E") {
		n, rest, ok := decimal(s)
		if !ok || n > len(rest) {
			return "", errMangled
		}
		segs = append(segs, rest[:n])
		s = rest[n:]
	}
	if len(segs) > 1 && legacyHash.MatchString(segs[len(segs)-1]) {
		segs = segs[:len(segs)-1]
	}
	return strings.Join(segs, "::"), nil
}

// demangleV0 reads the path of a v0 symbol, after "_R".
func demangleV0(s string) (string, error) {
	// An optional encoding version precedes the path.
	if _, rest, ok := decimal(s); ok {
		s = rest
	}
	d := v0{s: s}
	return d.path()
}

// v0 is a v0 symbol being read; s is everything after "_R" and the
// version, which a backref's offset counts from.
type v0 struct {
	s   string
	pos int
}

func (d *v0) path() (string, error) {
	if d.pos >= len(d.s) {
		return "", errMangled
	}
	tag := d.s[d.pos]
	d.pos++
	switch tag {
	case 'C':
		return d.ident()
	case 'N':
		if d.pos >= len(d.s) {
			return "", errMangled
		}
		ns := d.s[d.pos]
		d.pos++
		parent, err := d.path()
		if err != nil {
			return "", err
		}
		id, err := d.ident()
		if err != nil {
			return "", err
		}
		switch {
		case ns == 'C':
			return parent + "::{closure}", nil
		case ns >= 'A' && ns <= 'Z':
			return parent + "::{shim}", nil
		}
		return parent + "::" + id, nil
	case 'B':
		return d.backref(d.path)
	case 'I':
		// A generic path: its arguments are left out.
		p, err := d.path()
		if err != nil {
			return "", err
		}
		return p, d.genericArgs()
	case 'M':
		// An inherent impl: <Type>.
		if err := d.implPath(); err != nil {
			return "", err
		}
		t, err := d.typ()
		return "<" + t + ">", err
	case 'X':
		// A trait impl: <Type as Trait>.
		if err := d.implPath(); err != nil {
			return "", err
		}
		return d.qualified()
	case 'Y':
		// A trait definition: <Type as Trait>.
		return d.qualified()
	default:
		return "", errMangled
	}
}

// qualified reads a type and a trait path into <Type as Trait>.
func (d *v0) qualified() (string, error) {
	t, err := d.typ()
	if err != nil {
		return "", err
	}
	trait, err := d.path()
	return "<" + t + " as " + trait + ">", err
}

// implPath reads the [disambiguator] path of an impl, which names its
// module; the impl is known by its type.
func (d *v0) implPath() error {
	if d.pos < len(d.s) && d.s[d.pos] == 's' {
		d.pos++
		if _, err := d.base62(); err != nil {
			return err
		}
	}
	_, err := d.path()
	return err
}

// genericArgs skips generic arguments up to their closing E: lifetimes
// and types; a const argument is not read.
func (d *v0) genericArgs() error {
	for d.pos < len(d.s) && d.s[d.pos] != 'E' {
		if d.s[d.pos] == 'L' {
			d.pos++
			if _, err := d.base62(); err != nil {
				return err
			}
			continue
		}
		if _, err := d.typ(); err != nil {
			return err
		}
	}
	if d.pos >= len(d.s) {
		return errMangled
	}
	d.pos++
	return nil
}

// typ reads the types a function's path can hold: a basic type (one
// lowercase letter, kept as it is), a reference and a path. A tuple, an
// array, a pointer, a fn or a dyn type is not read.
func (d *v0) typ() (string, error) {
	if d.pos >= len(d.s) {
		return "", errMangled
	}
	switch c := d.s[d.pos]; {
	case c >= 'a' && c <= 'z':
		d.pos++
		return string(c), nil
	case c == 'R' || c == 'Q':
		d.pos++
		if d.pos < len(d.s) && d.s[d.pos] == 'L' {
			d.pos++
			if _, err := d.base62(); err != nil {
				return "", err
			}
		}
		t, err := d.typ()
		return "&" + t, err
	case c == 'B':
		d.pos++
		return d.backref(d.typ)
	case strings.IndexByte("CNIMXY", c) >= 0:
		return d.path()
	default:
		return "", errMangled
	}
}

// backref reads a backref's offset, after its B, and read at it.
func (d *v0) backref(read func() (string, error)) (string, error) {
	off, err := d.base62()
	if err != nil || off >= d.pos {
		return "", errMangled
	}
	saved := d.pos
	d.pos = off
	p, err := read()
	d.pos = saved
	return p, err
}

// ident reads [s<base62>] [u] <decimal> [_] <bytes>.
func (d *v0) ident() (string, error) {
	if d.pos < len(d.s) && d.s[d.pos] == 's' {
		d.pos++
		if _, err := d.base62(); err != nil {
			return "", err
		}
	}
	if d.pos < len(d.s) && d.s[d.pos] == 'u' {
		d.pos++ // Punycode is kept encoded.
	}
	n, rest, ok := decimal(d.s[d.pos:])
	if !ok {
		return "", errMangled
	}
	d.pos = len(d.s) - len(rest)
	if d.pos < len(d.s) && d.s[d.pos] == '_' {
		d.pos++
	}
	if d.pos+n > len(d.s) {
		return "", errMangled
	}
	id := d.s[d.pos : d.pos+n]
	d.pos += n
	return id, nil
}

// base62 reads a base-62 number ending in '_': "_" is 0, "<n>_" is n+1.
func (d *v0) base62() (int, error) {
	n, digits := 0, 0
	for d.pos < len(d.s) {
		c := d.s[d.pos]
		d.pos++
		var v int
		switch {
		case c == '_':
			if digits == 0 {
				return 0, nil
			}
			return n + 1, nil
		case c >= '0' && c <= '9':
			v = int(c - '0')
		case c >= 'a' && c <= 'z':
			v = int(c-'a') + 10
		case c >= 'A' && c <= 'Z':
			v = int(c-'A') + 36
		default:
			return 0, errMangled
		}
		n = n*62 + v
		digits++
	}
	return 0, errMangled
}

// decimal reads a decimal number with no leading zero (but "0").
func decimal(s string) (int, string, bool) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 || i > 1 && s[0] == '0' {
		return 0, s, false
	}
	n, err := strconv.Atoi(s[:i])
	return n, s[i:], err == nil
}
