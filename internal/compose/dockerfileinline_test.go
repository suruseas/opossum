package compose

import (
	"slices"
	"strings"
	"testing"
)

// `build.dockerfile_inline` is a Dockerfile written in the compose file, and
// docker compose builds it in place of any Dockerfile in the context (v5.5.1,
// measured with a context that has a Dockerfile of its own: the inline one is
// what is built). It was read and listed among the ignored fields, so `up` went
// looking for a Dockerfile in the context and failed with a message about a file
// the writer never meant to have.
func TestADockerfileWrittenInTheComposeFileIsRead(t *testing.T) {
	const inline = "FROM alpine:3\nRUN echo INLINE > /which\n"
	p, err := Load(writeTemp(t, "services:\n  app:\n    build:\n      context: .\n      dockerfile_inline: |\n        FROM alpine:3\n        RUN echo INLINE > /which\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	svc := p.Services["app"]
	if svc.Build == nil || svc.Build.DockerfileInline != inline {
		t.Fatalf("the inline Dockerfile is read as %q, want %q", svc.Build.DockerfileInline, inline)
	}
	if svc.Build.Dockerfile != "" {
		t.Errorf("a Dockerfile file name appeared out of an inline one: %q", svc.Build.Dockerfile)
	}
	// It is acted on, so it is no longer among the fields that are not.
	if slices.Contains(svc.Unsupported, "build.dockerfile_inline") {
		t.Errorf("dockerfile_inline is built now, yet it is listed as ignored: %v", svc.Unsupported)
	}
	// And `config` shows it, as a block, as docker compose does.
	out, err := RenderConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "dockerfile_inline: |") || !strings.Contains(out, "RUN echo INLINE > /which") {
		t.Errorf("config does not show the inline Dockerfile:\n%s", out)
	}
}

// Both a file name and a text: docker compose refuses the project when it is
// loaded (`declares mutualy exclusive dockerfile and dockerfile_inline`), and this
// does too. Either one alone loads, and an empty `dockerfile_inline` is not
// written at all.
func TestADockerfileAndAnInlineOneAreMutuallyExclusive(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build string
		want  string // empty: it loads
	}{
		{"both", "context: .\n      dockerfile: Dockerfile\n      dockerfile_inline: FROM alpine:3\n", "mutually exclusive"},
		{"only the file name", "context: .\n      dockerfile: Dockerfile\n", ""},
		{"only the text", "context: .\n      dockerfile_inline: FROM alpine:3\n", ""},
		{"the text is empty beside a file name", "context: .\n      dockerfile: Dockerfile\n      dockerfile_inline: \"\"\n", ""},
		{"the file name is empty beside a text", "context: .\n      dockerfile: \"\"\n      dockerfile_inline: FROM alpine:3\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, "services:\n  app:\n    build:\n      "+tc.build))
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("want it loaded, got %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "build.dockerfile_inline")):
				t.Errorf("want a refusal naming both keys and saying %q, got %v", tc.want, err)
			}
		})
	}
}
