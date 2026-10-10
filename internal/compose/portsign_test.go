package compose

import "testing"

// A plus sign in front of a port is no part of it: docker compose reads `+80:80` as the host port 80 (a minus sign is refused, and so is a `+` with nothing after it), wherever
// a port stands in the short form (the host or the container side, either end of a range, with an address or a protocol) and in the `published` and `target` of the long form.
// opossum passes the port on without the sign, as the runtime takes none. Measured, docker compose v5.5.1, `config --format json`, every row (#1849).
func TestAPlusSignInFrontOfAPortIsRead(t *testing.T) {
	for _, tc := range []struct {
		port string
		want string // the entry as it is passed on; "" when refused
	}{
		{`"+0:80"`, "80:80"},
		{`"+80:80"`, "80:80"},
		{`"80:+80"`, "80:80"},
		{`"+80-+81:80-81"`, "80-81:80-81"},
		{`"+80-81:80-81"`, "80-81:80-81"},
		{`"80-81:+80-+81"`, "80-81:80-81"},
		{`"+80"`, "80:80"},
		{`"127.0.0.1:+80:80"`, "127.0.0.1:80:80"},
		{`"127.0.0.1:+0:80"`, "127.0.0.1:80:80"},
		{`"+00:80"`, "80:80"},
		{`"+0-+1:80-81"`, "0-1:80-81"},
		{`"+0-1:80-81"`, "0-1:80-81"},
		{`"0-+1:80-81"`, "0-1:80-81"},
		{`"80:+80/tcp"`, "80:80/tcp"},
		{`"+80:80/udp"`, "80:80/udp"},
		{`"[::1]:+80:80"`, "[::1]:80:80"},
		{`"+1:80"`, "1:80"},
		{`"+65535:80"`, "65535:80"},
		{`"+8080-+8081:80-81"`, "8080-8081:80-81"},
		{`"80-81:+80"`, "80-81:80"},
		{`"+80-81:80"`, "80-81:80"},
		{`{target: 80, published: "+80"}`, "80:80"},
		{`{target: 80, published: "+0"}`, "80:80"},
		{`{target: 80, published: "+80-+81"}`, "80-81:80"},
		{`{target: "+80", published: 80}`, "80:80"},
		{`{target: "+80"}`, "80:80"},
		{`{target: 80, published: "+8080-+8081"}`, "8080-8081:80"},
		{`"-0:80"`, ""},
		{`"0x0:80"`, ""},
		{`"+0"`, ""},
		{`"+0:+0"`, ""},
		{`"+0.0:80"`, ""},
		{`"+65536:80"`, ""},
		{`"+0:80-81"`, ""},
		{`"1e1:80"`, ""},
		{`"+-1:80"`, ""},
		{`"++80:80"`, ""},
		{`"80:++80"`, ""},
		{`"+ 80:80"`, ""},
		{`"+80+:80"`, ""},
		{`"+80-++81:80-81"`, ""},
	} {
		t.Run(tc.port, func(t *testing.T) {
			p, err := Load(writeTemp(t, "services:\n  web:\n    image: alpine\n    ports: ["+tc.port+"]\n"))
			if tc.want == "" {
				if err == nil {
					t.Errorf("a port docker refuses is read: %v", p.Services["web"].Ports)
				}
				return
			}
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := p.Services["web"].Ports; len(got) != 1 || got[0] != tc.want {
				t.Errorf("ports = %v, want [%s]", got, tc.want)
			}
		})
	}
}

// The sign is gone before two entries are compared: `80:80` and `+80:80` are the one port, as docker compose folds them (measured, v5.5.1; #1849).
func TestAPortWithAPlusSignAndWithoutAreOnePort(t *testing.T) {
	p, err := Load(writeTemp(t, "services:\n  web:\n    image: alpine\n    ports: [\"80:80\", \"+80:80\"]\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := p.Services["web"].Ports; len(got) != 1 || got[0] != "80:80" {
		t.Errorf("ports = %v, want [80:80]", got)
	}
}
