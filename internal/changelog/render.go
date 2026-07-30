package changelog

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// unreleasedHeading is the section the preview keeps in sync with changelog.d.
const unreleasedHeading = "## [Unreleased]"

// versionRe matches a released section heading, e.g. `## [0.16.0] - 2026-07-29`.
var versionRe = regexp.MustCompile(`(?m)^## \[[0-9]`)

// linkRe matches a version's link-reference definition at the foot of the file.
// Every heading in the changelog is a reference link; a section without one
// renders as literal `[0.17.0]` text on the published page.
var linkRe = regexp.MustCompile(`(?m)^\[([^\]]+)\]: (\S+)$`)

// Unreleased returns the current `## [Unreleased]` body from a CHANGELOG (the
// text between that heading and the first released one), with surrounding blank
// lines trimmed.
func Unreleased(changelog string) string {
	i := strings.Index(changelog, unreleasedHeading)
	if i < 0 {
		return ""
	}
	rest := changelog[i+len(unreleasedHeading):]
	if loc := versionRe.FindStringIndex(rest); loc != nil {
		rest = rest[:loc[0]]
	}
	return strings.Trim(rest, "\n")
}

// WithUnreleased returns the changelog with its `## [Unreleased]` body replaced by
// body. An empty body leaves the heading with nothing under it, which is what an
// empty changelog.d means.
func WithUnreleased(changelog, body string) (string, error) {
	i := strings.Index(changelog, unreleasedHeading)
	if i < 0 {
		return "", fmt.Errorf("no %q heading in the changelog", unreleasedHeading)
	}
	head := changelog[:i+len(unreleasedHeading)]
	rest := changelog[i+len(unreleasedHeading):]
	tail := ""
	if loc := versionRe.FindStringIndex(rest); loc != nil {
		tail = rest[loc[0]:]
	}
	if body == "" {
		return head + "\n\n" + tail, nil
	}
	return head + "\n\n" + strings.TrimRight(body, "\n") + "\n\n" + tail, nil
}

// Release turns the fragments into a `## [version] - date` section, inserted
// above the most recent released one, and returns the new changelog. The
// Unreleased section is emptied, because its contents just became the release,
// and the link-reference definitions at the foot are updated so the new heading
// renders as a link and `[Unreleased]` compares against the new tag.
func Release(changelog string, frags []Fragment, version, date string) (string, error) {
	body := RenderBody(frags)
	if body == "" {
		return "", fmt.Errorf("no fragments to release")
	}
	if versionExists(changelog, version) {
		return "", fmt.Errorf("%s is already in the changelog — releasing it again would add a second section for it", version)
	}
	cleared, err := WithUnreleased(changelog, "")
	if err != nil {
		return "", err
	}
	previous := latestVersion(cleared)
	section := fmt.Sprintf("## [%s] - %s\n\n%s", version, date, strings.TrimRight(body, "\n")+"\n")
	loc := versionRe.FindStringIndex(cleared)
	var out string
	if loc == nil {
		out = strings.TrimRight(cleared, "\n") + "\n\n" + section
	} else {
		out = cleared[:loc[0]] + section + "\n" + cleared[loc[0]:]
	}
	return withLinks(out, version, previous), nil
}

// versionExists reports whether the changelog already has a section for version.
func versionExists(changelog, version string) bool {
	return regexp.MustCompile(`(?m)^## \[` + regexp.QuoteMeta(version) + `\]`).MatchString(changelog)
}

// latestVersion returns the newest released version in the changelog, or "".
func latestVersion(changelog string) string {
	m := regexp.MustCompile(`(?m)^## \[([0-9][^\]]*)\]`).FindStringSubmatch(changelog)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// withLinks adds the new version's link-reference definition and re-points
// [Unreleased] at it, mirroring how the block was maintained by hand. The URLs
// are derived from the existing entries, so the repository never has to be
// hard-coded here.
func withLinks(changelog, version, previous string) string {
	links := linkRe.FindAllStringSubmatchIndex(changelog, -1)
	if len(links) == 0 {
		return changelog // no link block to maintain
	}
	compare := ""
	for _, m := range links {
		name, url := changelog[m[2]:m[3]], changelog[m[4]:m[5]]
		if strings.Contains(url, "/compare/") && name != "Unreleased" {
			compare = url[:strings.Index(url, "/compare/")+len("/compare/")]
			break
		}
	}
	if compare == "" {
		return changelog
	}
	// Idempotent: a definition already present is left as it is, so re-running
	// after a partial failure doesn't stack duplicates.
	if regexp.MustCompile(`(?m)^\[` + regexp.QuoteMeta(version) + `\]: `).MatchString(changelog) {
		return regexp.MustCompile(`(?m)^\[Unreleased\]: \S+$`).ReplaceAllString(changelog,
			fmt.Sprintf("[Unreleased]: %sv%s...HEAD", compare, version))
	}
	newLink := fmt.Sprintf("[%s]: %sv%s...v%s", version, compare, previous, version)
	if previous == "" {
		newLink = fmt.Sprintf("[%s]: %sv%s", version, strings.Replace(compare, "/compare/", "/releases/tag/", 1), version)
	}
	// Re-point [Unreleased] at the new tag, then insert the new definition above
	// the previous newest one (the block is newest-first).
	out := regexp.MustCompile(`(?m)^\[Unreleased\]: \S+$`).ReplaceAllString(changelog,
		fmt.Sprintf("[Unreleased]: %sv%s...HEAD", compare, version))
	if previous == "" {
		return strings.TrimRight(out, "\n") + "\n" + newLink + "\n"
	}
	prevDef := fmt.Sprintf("[%s]: ", previous)
	if i := strings.Index(out, "\n"+prevDef); i >= 0 {
		return out[:i+1] + newLink + "\n" + out[i+1:]
	}
	return strings.TrimRight(out, "\n") + "\n" + newLink + "\n"
}

// Consume deletes the fragment files that have been folded into a release.
func Consume(frags []Fragment) error {
	for _, f := range frags {
		if err := os.Remove(f.Path); err != nil {
			return err
		}
	}
	return nil
}
