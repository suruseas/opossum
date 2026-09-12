package compose

import (
	"strings"
	"testing"
)

// `mac_address` (#879) is read and given to the runtime as the MAC of the
// service's first network; docker compose keeps it as written and refuses a
// bad one when the container is made (`invalid MAC address`), the runtime
// refuses any spelling but six hex pairs (`invalid MAC address format …,
// expected format: XX:XX:XX:XX:XX:XX`) — both said at load here.
func TestMacAddressIsReadAndNotListedAsIgnored(t *testing.T) {
	p, err := Load(writeTemp(t, "services:\n  web:\n    image: a\n    mac_address: 02:42:ac:11:00:77\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := p.Services["web"].MacAddress; got != "02:42:ac:11:00:77" {
		t.Errorf("mac_address = %q, want the colon form", got)
	}
	if indexOfStr(p.Services["web"].Unsupported, "mac_address") >= 0 {
		t.Errorf("mac_address is read and must not be listed as ignored, got %v", p.Services["web"].Unsupported)
	}
	// Per-network mac_address is still read past, and named.
	p, err = Load(writeTemp(t, "services:\n  web:\n    image: a\n    networks:\n      back:\n        mac_address: 02:42:ac:11:00:66\nnetworks:\n  back: {}\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if p.Services["web"].MacAddress != "" || indexOfStr(p.Services["web"].Unsupported, "networks.back.mac_address") < 0 {
		t.Errorf("a per-network mac_address must not become the service's, and must be listed: mac=%q ignored=%v", p.Services["web"].MacAddress, p.Services["web"].Unsupported)
	}
}

// Every usual spelling is taken and carried in the colon form the runtime
// reads (`expected format: XX:XX:XX:XX:XX:XX`).
func TestMacAddressSpellingsAreNormalised(t *testing.T) {
	for _, spelling := range []string{"02:42:ac:11:00:77", "02-42-ac-11-00-77", "0242.ac11.0077", "02:42:AC:11:00:77"} {
		t.Run(spelling, func(t *testing.T) {
			p, err := Load(writeTemp(t, "services:\n  web:\n    image: a\n    mac_address: \""+spelling+"\"\n"))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := p.Services["web"].MacAddress; got != "02:42:ac:11:00:77" {
				t.Errorf("mac_address %q = %q, want the colon form 02:42:ac:11:00:77", spelling, got)
			}
		})
	}
}

// `opossum config` shows the address, as it shows the other settings `up`
// sends (before this it was among the ignored keys; read, it must not vanish).
func TestRenderConfigShowsMacAddress(t *testing.T) {
	p, err := Load(writeTemp(t, "services:\n  web:\n    image: a\n    mac_address: 02-42-ac-11-00-77\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	out, err := RenderConfig(p)
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	if !strings.Contains(out, "mac_address: 02:42:ac:11:00:77") {
		t.Errorf("config output should render the normalised mac_address, got:\n%s", out)
	}
}

func TestMacAddressRefusals(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"not a MAC", "services:\n  web:\n    image: a\n    mac_address: notamac\n", `mac_address "notamac" is not a 48-bit MAC address`},
		{"too short", "services:\n  web:\n    image: a\n    mac_address: 02:42:ac\n", "is not a 48-bit MAC address"},
		{"an EUI-64, which the runtime does not take", "services:\n  web:\n    image: a\n    mac_address: 02:42:ac:11:00:77:00:01\n", "is not a 48-bit MAC address"},
		{"with network_mode none", "services:\n  web:\n    image: a\n    network_mode: none\n    mac_address: 02:42:ac:11:00:77\n", "network_mode: none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want an error containing %q, got: %v", tc.want, err)
			}
		})
	}
}
