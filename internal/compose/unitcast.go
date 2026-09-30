package compose

import (
	"math"
	"math/big"
	"strconv"
	"strings"
)

// bytesUnits are the suffixes docker compose reads a byte size with, written in
// any case (measured, v5.5.1: `1G`, `1gb`, `1KiB` and `1 GiB` read; `1e`, `1ki`,
// `1mi`, `1bytes` and `1G B` do not). Nothing after the number is a multiplier of
// one.
var bytesUnits = map[string]bool{
	"": true, "b": true,
	"k": true, "kb": true, "kib": true,
	"m": true, "mb": true, "mib": true,
	"g": true, "gb": true, "gib": true,
	"t": true, "tb": true, "tib": true,
	"p": true, "pb": true, "pib": true,
}

// bytesOK reports whether s is a string docker compose reads as a byte size
// (`shm_size`, `mem_reservation`, `memswap_limit`, `mem_swappiness`), as measured
// on v5.5.1 (testdata/unit-forms.json holds the sweep). A whole number reads as
// itself, a `-1` included (`-1`, `-2` and `+2` are numbers, not sizes with a
// sign). Anything else is a number as Go reads a float — a fraction, an exponent,
// digit separators (`1.5`, `1e3`, `1_000`), a hex float with its p exponent
// (`0x1p3`), and none too big to be a float (`1e400`) — with at most one space and
// then a unit. `0x10` alone is not one, and `nan` and `inf` are numbers only where
// a space follows them (see the branch below). It cannot be negative (`-1.5`, `-1g`
// and `-1e3` are refused, `-0g` is not), and nothing may surround it.
//
// Not the same as docker compose's on one point: the Kelvin sign U+212A folds to a
// `k` in a suffix docker compose measures by its bytes, so `1K` with it reads there
// and not here. Nothing else was found to differ in a sweep of 1328 more forms.
func bytesOK(s string) bool {
	if _, err := strconv.ParseInt(s, 10, 64); err == nil {
		return true
	}
	var num, unit string
	if strings.Contains(s, " ") {
		// One space splits the number from the unit, and the unit may be none: what
		// is before it is read as a float whole, which is why `nan ` and `inf B` read
		// where `nan` and `nanB` do not.
		if strings.Count(s, " ") > 1 {
			return false
		}
		i := strings.Index(s, " ")
		num, unit = s[:i], strings.ToLower(s[i+1:])
	} else {
		i := len(s)
		for i > 0 && isASCIILetter(s[i-1]) {
			i--
		}
		num, unit = s[:i], strings.ToLower(s[i:])
	}
	if !bytesUnits[unit] {
		return false
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil || f < 0 {
		return false
	}
	return true
}

func isASCIILetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// durationUnits are the units docker compose reads a duration with: Go's
// time.ParseDuration's, and the day and the week it adds (measured, v5.5.1: `1d`
// and `1w` read; `1S`, `1H`, `1min` and `1 s` do not).
var durationUnits = map[string]int64{
	"ns": 1, "us": 1e3, "µs": 1e3, "μs": 1e3, "ms": 1e6,
	"s": 1e9, "m": 60e9, "h": 3600e9, "d": 86400e9, "w": 604800e9,
}

// durationOK reports whether s is a string docker compose reads as a duration
// (`stop_grace_period`): an optional sign, then `0`, or one or more numbers each
// with a unit, as time.ParseDuration reads them (a fraction may have no digits
// before or after the point, but one of them has some), and no space or other
// character anywhere. A bare number is not a duration (`10` is refused).
//
// A fraction is read exactly, where docker compose floors each component's through
// float64: within a nanosecond of the int64 limit a value that carries one is
// refused here and read there (twelve forms of that kind are pinned in unitcast_test.go, and there are more).
func durationOK(s string) bool {
	if s != "" && (s[0] == '-' || s[0] == '+') {
		s = s[1:]
	}
	if s == "0" {
		return true
	}
	if s == "" {
		return false
	}
	total := new(big.Rat) // the size of it in nanoseconds, whatever the sign
	for s != "" {
		i, digits := 0, 0
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
			digits++
		}
		if i < len(s) && s[i] == '.' {
			i++
			for i < len(s) && s[i] >= '0' && s[i] <= '9' {
				i++
				digits++
			}
		}
		if digits == 0 {
			return false
		}
		n, ok := new(big.Rat).SetString(strings.TrimSuffix(s[:i], "."))
		if !ok {
			return false
		}
		s = s[i:]
		u := 0
		for u < len(s) && s[u] != '.' && (s[u] < '0' || s[u] > '9') {
			u++
		}
		ns, known := durationUnits[s[:u]]
		if !known {
			return false
		}
		total.Add(total, n.Mul(n, new(big.Rat).SetInt64(ns)))
		s = s[u:]
	}
	// docker compose refuses one that does not fit the nanoseconds of an int64, the
	// most negative one included (measured: 9223372036854775808ns and
	// -9223372036854775808ns are refused, 9223372036854775807ns is read).
	return total.Cmp(new(big.Rat).SetInt64(math.MaxInt64)) <= 0
}
