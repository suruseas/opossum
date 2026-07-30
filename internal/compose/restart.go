package compose

import (
	"fmt"
	"strconv"
	"strings"
)

// Restart policies, as compose spells them.
const (
	RestartNo            = "no"
	RestartAlways        = "always"
	RestartUnlessStopped = "unless-stopped"
	RestartOnFailure     = "on-failure"
)

// RestartPolicy is a parsed `restart:` value.
type RestartPolicy struct {
	Mode     string // one of the constants above; "" when the service set nothing
	MaxRetry int    // on-failure:N — 0 means "no limit given"
}

// Wants reports whether the policy asks for anything to be restarted at all.
func (p RestartPolicy) Wants() bool {
	return p.Mode == RestartAlways || p.Mode == RestartUnlessStopped || p.Mode == RestartOnFailure
}

// ParseRestart reads a `restart:` value. An empty value is "no policy", which is
// the same as `no` — nothing is watched.
func ParseRestart(v string) (RestartPolicy, error) {
	v = strings.TrimSpace(v)
	switch v {
	case "":
		return RestartPolicy{}, nil
	case RestartNo, RestartAlways, RestartUnlessStopped:
		return RestartPolicy{Mode: v}, nil
	case RestartOnFailure:
		return RestartPolicy{Mode: RestartOnFailure}, nil
	}
	if rest, ok := strings.CutPrefix(v, RestartOnFailure+":"); ok {
		n, err := strconv.Atoi(strings.TrimSpace(rest))
		if err != nil || n < 0 {
			return RestartPolicy{}, fmt.Errorf("restart: %q has an unreadable retry count (want on-failure:N)", v)
		}
		return RestartPolicy{Mode: RestartOnFailure, MaxRetry: n}, nil
	}
	return RestartPolicy{}, fmt.Errorf("restart: %q is not a policy (want no, always, unless-stopped, or on-failure[:N])", v)
}

// RestartPolicy parses the service's `restart:`; callers that already validated
// at load time can ignore the error.
func (s *Service) RestartPolicy() (RestartPolicy, error) { return ParseRestart(s.Restart) }
