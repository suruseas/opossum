package compose

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// normalizePortSpec writes a checked short-form port the way the runtime
// takes it, dropping the parts that are written as nothing: an empty host
// address (`::80`, `:8080:80` — a host address of `:` or nothing) and an
// empty protocol after a `/` (`80/`, `80:80/`); an empty host port left
// behind (`:80`) is mirrored from the container port later, as a lone
// container port is. docker compose reads each
// as the ports that are there (`::80` and `80/` are the container port 80,
// `:8080:80` and `80:80/` are `8080:80` and `80:80`; measured on v5.5.0);
// Apple `container run -p` refuses the spellings (`invalid publish
// value`, measured on 1.3.1), so passed on — as written, or as `:80:80`
// where the mirroring of a lone container port kept the empty address —
// they failed at `up`. A real address is kept, and an IPv6 one written
// without brackets is bracketed (`::1:8080:80` → `[::1]:8080:80`; see
// below). Called after checkPortSpec: dropping the empty
// parts first would fold an address that is only colons (`:::80`) into a
// port the check would then pass.
func normalizePortSpec(spec string) string {
	s, proto := spec, ""
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s, proto = s[:i], s[i+1:]
	}
	parts := strings.Split(s, ":")
	// An IPv6 host address written without brackets (`::1:8080:80`, which
	// docker compose reads as the address `::1`) is passed bracketed, the
	// only spelling the runtime takes (`container run -p '::1:8080:80'` is
	// `invalid publish value`, `[::1]:8080:80` runs; measured on 1.3.1) —
	// as the long form already passes an unbracketed `host_ip`. Before
	// the empty parts are dropped, so that the any address written as
	// nothing but colons (`:::8080:80`, the address `::` to docker
	// compose) is an address, not two empty parts.
	if len(parts) > 2 {
		if addr := strings.Join(parts[:len(parts)-2], ":"); strings.Contains(addr, ":") && !strings.HasPrefix(addr, "[") {
			parts = append([]string{"[" + addr + "]"}, parts[len(parts)-2:]...)
		}
	}
	if len(parts) > 2 {
		empty := true
		for _, a := range parts[:len(parts)-2] {
			if a != "" {
				empty = false
				break
			}
		}
		if empty {
			parts = parts[len(parts)-2:]
		}
	}
	s = strings.Join(parts, ":")
	if proto != "" {
		s += "/" + proto
	}
	return s
}

// checkPortSpec reads a short-form port (`[host_ip:]host:container[/proto]`,
// each port a number or a `low-high` range) the way docker compose validates
// one, and says what is wrong without reading the value back — a `${VAR}`
// can stand anywhere in it. What it refuses used to reach `container run -p`
// as written: a container port that is a word, a fraction, 0, above 65535,
// padded with spaces or in hex; a host port above 65535 or a range written
// high-low; a host address that is not an IP (`localhost`, `*`, a fourth
// port); a protocol other than tcp, udp or sctp; a container range with a
// host range of another length; and a spec with no
// container port at all (`80:`, `:`). The host port may be left out (`:80`,
// `ip::80`): the engine picks one, and normalizePort records that.
func checkPortSpec(spec string) error {
	s := spec
	if i := strings.IndexByte(s, '/'); i >= 0 {
		proto := strings.ToLower(s[i+1:])
		if proto != "" && proto != "tcp" && proto != "udp" && proto != "sctp" {
			return errors.New("the protocol after `/` must be tcp, udp or sctp")
		}
		s = s[:i]
	}
	// From the right: the container port, then the host port, then whatever
	// is left is the host address (an IPv6 one may be bracketed or not).
	var container, host, addr string
	switch parts := strings.Split(s, ":"); len(parts) {
	case 1:
		container = parts[0]
	case 2:
		host, container = parts[0], parts[1]
	default:
		container = parts[len(parts)-1]
		host = parts[len(parts)-2]
		addr = strings.Join(parts[:len(parts)-2], ":")
	}
	if container == "" {
		return errors.New("no container port — write `host:container`, or the container port alone")
	}
	cLow, cHigh, err := portRange(container, 1)
	if err != nil {
		return fmt.Errorf("the container port %v", err)
	}
	hLow, hHigh := 0, 0
	if host != "" {
		if hLow, hHigh, err = portRange(host, 0); err != nil {
			return fmt.Errorf("the host port %v", err)
		}
	}
	// A container range needs a host range of the same length (a single
	// container port takes a single host port or a range of them, and the
	// engine spreads the range over that one port). With no host port at
	// all the range is mirrored later, so it needs nothing here.
	if cHigh > cLow && host != "" && hHigh-hLow != cHigh-cLow {
		return errors.New("a container port range needs a host port range of the same length, as in `8000-8010:80-90`")
	}
	if addr != "" {
		a := strings.TrimSuffix(strings.TrimPrefix(addr, "["), "]")
		if net.ParseIP(a) == nil {
			return errors.New("the host address before the ports must be an IP address, as in `127.0.0.1:8080:80` — a fourth `:` part or a host name is not one")
		}
	}
	return nil
}

// portRange reads `n` or `low-high`: digits only (no sign, space or hex),
// each between min and 65535, low not above high.
func portRange(s string, min int) (low, high int, err error) {
	word := func() (int, int, error) {
		if min == 0 {
			return 0, 0, errors.New("must be a number from 0 to 65535, or a range `low-high` of them")
		}
		return 0, 0, errors.New("must be a number from 1 to 65535, or a range `low-high` of them")
	}
	one := func(t string) (int, bool) {
		if t == "" {
			return 0, false
		}
		for _, c := range t {
			if c < '0' || c > '9' {
				return 0, false
			}
		}
		n, err := strconv.Atoi(t)
		return n, err == nil && n >= min && n <= 65535
	}
	lowS, highS, isRange := strings.Cut(s, "-")
	var ok bool
	if low, ok = one(lowS); !ok {
		return word()
	}
	if !isRange {
		return low, low, nil
	}
	if high, ok = one(highS); !ok || high < low {
		return word()
	}
	return low, high, nil
}
