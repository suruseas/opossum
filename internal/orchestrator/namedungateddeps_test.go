package orchestrator_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/orchestrator"
)

// A service with no `profiles:` that depends on one behind a profile is read without a name, so naming it (or the dependency too) is no way out: docker compose v5.5.1
// refuses `stop other` and `stop other db` alike (measured), and the refusal does not suggest what does not work (#1870). The gated service that only its name enables keeps the
// advice, which does work there (TestNamingBothTheGatedServiceAndItsDependencyEnablesBoth).
func TestTheRefusalDoesNotSayToNameAnUngatedServiceItIsAbout(t *testing.T) {
	for _, named := range [][]string{{"other"}, {"web"}} {
		for _, tc := range namedCommands {
			t.Run(strings.Join(named, "+")+"/"+tc.name, func(t *testing.T) {
				rt, _ := fakeShim(t)
				p := project("demo", map[string]*compose.Service{
					"web":   {Image: "alpine:3.20", Profiles: []string{"g"}, DependsOn: compose.DependsOn{{Name: "db"}}},
					"db":    {Image: "alpine:3.20", Profiles: []string{"g"}},
					"other": {Image: "alpine:3.20", DependsOn: compose.DependsOn{{Name: "db"}}},
				})
				o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
				err := tc.call(o, named)
				if err == nil || !strings.Contains(err.Error(), `whose profile is not active`) || !strings.Contains(err.Error(), `"db"`) {
					t.Fatalf("want the refusal over the dependency, got: %v", err)
				}
				if strings.Contains(err.Error(), "name it explicitly") {
					t.Errorf("says to name what does not help an ungated service: %v", err)
				}
			})
		}
	}
}

// …and the advice stays where it works: `web[g]` -> `mid[g]` -> `db[h]`, `stop web` is refused for `mid`, which only the name of `web` brought in, and `stop web db` goes
// on (docker compose v5.5.1, measured). A refusal that never advises is not the fix (#1870).
func TestTheRefusalStillSaysToNameTheDependencyOfAServiceTheNameBroughtIn(t *testing.T) {
	for _, tc := range namedCommands {
		t.Run(tc.name, func(t *testing.T) {
			rt, _ := fakeShim(t)
			p := project("demo", map[string]*compose.Service{
				"web": {Image: "alpine:3.20", Profiles: []string{"g"}, DependsOn: compose.DependsOn{{Name: "mid"}}},
				"mid": {Image: "alpine:3.20", Profiles: []string{"g"}, DependsOn: compose.DependsOn{{Name: "db"}}},
				"db":  {Image: "alpine:3.20", Profiles: []string{"h"}},
			})
			o := orchestrator.New(p, rt, "opossum", &bytes.Buffer{})
			err := tc.call(o, []string{"web"})
			if err == nil || !strings.Contains(err.Error(), `service "mid" depends on "db", whose profile is not active — name it explicitly`) {
				t.Errorf("want the refusal for mid to say to name it, got: %v", err)
			}
		})
	}
}
