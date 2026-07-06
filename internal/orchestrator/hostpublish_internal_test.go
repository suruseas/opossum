package orchestrator

import (
	"slices"
	"testing"
)

func TestHostPublishAddrs(t *testing.T) {
	cases := []struct {
		desc string
		in   []string
		want []string
	}{
		{"host:container", []string{"4200:4200"}, []string{"localhost:4200"}},
		{"container-port proto never leaks into host addr", []string{"8080:80/tcp"}, []string{"localhost:8080"}},
		{"ip:host:container keeps ip", []string{"127.0.0.1:8080:80"}, []string{"127.0.0.1:8080"}},
		{"0.0.0.0 normalizes to localhost", []string{"0.0.0.0:8080:80"}, []string{"localhost:8080"}},
		{"container-only is skipped (host port unknown)", []string{"80"}, nil},
		{"multiple", []string{"4200:4200", "9229:9229"}, []string{"localhost:4200", "localhost:9229"}},
		{"port range is kept as-is", []string{"8000-8005:8000-8005"}, []string{"localhost:8000-8005"}},
		{"surrounding whitespace is trimmed", []string{" 4200:4200 "}, []string{"localhost:4200"}},
		{"bracketed IPv6 host", []string{"[::1]:8080:80"}, []string{"[::1]:8080"}},
		{"bare IPv6 host", []string{"::1:8080:80"}, []string{"[::1]:8080"}},
		{"IPv6 wildcard :: normalizes to localhost", []string{"[::]:8080:80/tcp"}, []string{"localhost:8080"}},
		{"none", nil, nil},
	}
	for _, c := range cases {
		if got := hostPublishAddrs(c.in); !slices.Equal(got, c.want) {
			t.Errorf("%s: hostPublishAddrs(%v) = %v, want %v", c.desc, c.in, got, c.want)
		}
	}
}
