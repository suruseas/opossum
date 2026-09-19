package orchestrator

import (
	"fmt"
	"sort"
	"strings"
)

// LeftUnderFormerName is what this compose file's project still has in the
// runtime under another name — the name opossum used for it before
// COMPOSE_PROJECT_NAME was read (#1122): the file's `name:`, or its directory.
//
// A project's name is how every command finds what it acts on: its containers
// are `<service>.<project>.<domain>`, its volumes `<project>_<volume>`. So the
// day the name changes, everything made under the old one goes out of reach
// without anything having happened to it (measured: `ps` lists nothing, `down`
// stops nothing while the old container runs on, and `up` brings up a second
// project over an empty volume, beside the one with the data in it). Nothing
// here acts on those; they are found so that they can be pointed at.
//
// Only what this file accounts for is looked for — a container of one of its
// services, carrying the old name's label, and a volume it declares — so a
// different project that merely shares the old name is not reported beyond the
// names the two have in common.
func (o *Orchestrator) LeftUnderFormerName(former string) (containers, volumes []string) {
	if former == "" || former == o.Project.Name {
		return nil, nil
	}
	was := *o
	project := *o.Project
	project.Name = former
	was.Project = &project

	wanted := map[string]bool{}
	for name := range o.Project.Services {
		wanted[was.containerName(name)] = true
	}
	for _, c := range o.rt.List() {
		if wanted[c.Name] && c.Labels[projectLabel] == former {
			containers = append(containers, c.Name)
		}
	}
	there := map[string]bool{}
	for _, v := range o.rt.ListVolumes() {
		there[v] = true
	}
	for key, decl := range o.Project.Volumes {
		// A volume with a name of its own, or an external one, is called the
		// same whatever the project is: it has not gone anywhere.
		if decl.External || decl.Name != "" {
			continue
		}
		if v := was.volumeName(key); there[v] {
			volumes = append(volumes, v)
		}
	}
	sort.Strings(containers)
	sort.Strings(volumes)
	return containers, volumes
}

// FormerNameNote says what LeftUnderFormerName found, and how to reach it. ""
// when there is nothing to say. flags is the rest of this run's root flags,
// spelled as typed (" -f x.yaml --dns-domain d"), which the command it says
// has to carry to read the same file and reach the same names.
func FormerNameNote(current, former string, containers, volumes []string, flags string) string {
	if len(containers) == 0 && len(volumes) == 0 {
		return ""
	}
	var has []string
	if len(containers) > 0 {
		has = append(has, "containers ("+strings.Join(containers, ", ")+")")
	}
	if len(volumes) > 0 {
		has = append(has, "volumes ("+strings.Join(volumes, ", ")+")")
	}
	note := fmt.Sprintf("note: this project is %q — COMPOSE_PROJECT_NAME names it, as it does for docker compose. "+
		"opossum used to call it %q, and there are still %s under that name, out of reach of every command that is not told `-p %s`. "+
		"`opossum -p %s%s down` stops that project — its restart supervisor and its network go with it — and removes its containers, "+
		"leaving its volumes and the images it built; add `-v` for the volumes and `--rmi local` for the images.",
		current, former, strings.Join(has, " and "), former, former, flags)
	if len(volumes) > 0 {
		// The part that costs someone their data if it goes unsaid.
		note += fmt.Sprintf(" The data is in %q's volumes: an `up` here starts %q with empty ones.", former, current)
	}
	return note
}

// ShellWord spells s so that a POSIX shell reads it back as one word: as it is
// where nothing in it means anything to a shell, in single quotes otherwise.
func ShellWord(s string) string {
	plain := s != ""
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_@%+=:,./-", r)) {
			plain = false
			break
		}
	}
	if plain {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
