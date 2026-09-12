package compose

import (
	"strings"
	"testing"
)

// `labels` (#882) is read: the mapping form sorted by key, the list form as
// written, a bare `key` as the empty value, values interpolated — as docker
// compose reads them (oracle: sweeps/labels-oracle).
func TestLabelsAreReadInBothForms(t *testing.T) {
	t.Setenv("OPOSSUM_T_WHO", "someone")
	p, err := Load(writeTemp(t, "services:\n  web:\n    image: a\n    labels:\n      tier: front\n      app.role: \"${OPOSSUM_T_WHO}\"\n      count: 1\n  list:\n    image: a\n    labels:\n      - \"app.tier=front\"\n      - flagonly\nnetworks:\n  back:\n    labels:\n      net.tier: back\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := strings.Join(p.Services["web"].Labels, ","); got != "app.role=someone,count=1,tier=front" {
		t.Errorf("mapping labels = %q, want sorted key=value with the value interpolated", got)
	}
	if got := strings.Join(p.Services["list"].Labels, ","); got != "app.tier=front,flagonly=" {
		t.Errorf("list labels = %q, want as written with a bare key as the empty value", got)
	}
	if got := strings.Join(p.Networks["back"].Labels, ","); got != "net.tier=back" {
		t.Errorf("network labels = %q", got)
	}
	for _, svc := range []string{"web", "list"} {
		if indexOfStr(p.Services[svc].Unsupported, "labels") >= 0 {
			t.Errorf("labels is read and must not be listed as ignored for %s, got %v", svc, p.Services[svc].Unsupported)
		}
	}
	if indexOfStr(p.Unsupported, "networks.back.labels") >= 0 {
		t.Errorf("a network's labels are read and must not be listed, got %v", p.Unsupported)
	}
	out, err := RenderConfig(p)
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	for _, want := range []string{"app.role: someone", "flagonly: \"\"", "net.tier: back"} {
		if !strings.Contains(out, want) {
			t.Errorf("config output should show %q, got:\n%s", want, out)
		}
	}
}

// The mapping's values: a null is the empty value (docker compose reads
// `c: ~` as `""`), an alias is read through, and a value holding `=` is
// kept whole — `config` cuts at the first `=`, as docker compose does.
func TestLabelValuesAreReadAsDockerComposeReadsThem(t *testing.T) {
	p, err := Load(writeTemp(t, "x-v: &v shared\nservices:\n  web:\n    image: a\n    labels:\n      nul: ~\n      e: \"\"\n      a: *v\n      m: b=c\n  list:\n    image: a\n    labels:\n      - *v\n      - \"k=b=c\"\n      - a=1\n      - a=2\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := strings.Join(p.Services["web"].Labels, ","); got != "a=shared,e=,m=b=c,nul=" {
		t.Errorf("mapping values = %q, want a null and an empty string as the empty value, the alias read through, and b=c kept whole", got)
	}
	if got := strings.Join(p.Services["list"].Labels, ","); got != "shared=,k=b=c,a=1,a=2" {
		t.Errorf("list values = %q, want the alias read through, k=b=c as written, and a repeated key kept twice", got)
	}
	out, err := RenderConfig(p)
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	// A key repeated in the list is shown once with the last value, as the
	// runtime keeps the last `-l` and docker compose's config shows it.
	for _, want := range []string{"m: b=c", "k: b=c", "nul: \"\"", "a: shared", "a: \"2\""} {
		if !strings.Contains(out, want) {
			t.Errorf("config output should show %q (cut at the first =), got:\n%s", want, out)
		}
	}
}

func TestLabelsShapesAreStillRefused(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"a single value", "services:\n  web:\n    image: a\n    labels: x\n", "labels must be a mapping or a list"},
		{"a list item that is a mapping", "services:\n  web:\n    image: a\n    labels:\n      - {a: b}\n", "must be a string"},
		{"a mapping value that is a list", "services:\n  web:\n    image: a\n    labels:\n      a: [1]\n", "labels entry a"},
		{"a network's labels as a single value", "services:\n  web:\n    image: a\nnetworks:\n  back:\n    labels: x\n", "labels must be a mapping or a list"},
		{"a network's labels list item that is a mapping", "services:\n  web:\n    image: a\nnetworks:\n  back:\n    labels:\n      - {a: b}\n", "must be a string"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want an error containing %q, got: %v", tc.want, err)
			}
		})
	}
}
