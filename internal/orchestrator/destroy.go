package orchestrator

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/suruseas/opossum/internal/compose"
	"github.com/suruseas/opossum/internal/workspace"
)

// Destroy is the way out of a trial run: everything opossum made for this
// project, gone, in one command. It exists because "easy to try" is only half a
// promise — the other half is being able to remove it without a checklist. `down`
// leaves images, volumes, the generated overlay, the project state directory and
// the restart supervisor behind, each with its own flag or manual step.
//
// The safety of the command rests on one line: it removes what opossum created,
// and nothing the user wrote. Compose files, `.env`, and sources are never
// touched. Anything shared beyond this project — the DNS domain, the builder
// cache, another project's containers — is out of scope by construction: the
// container sweep filters on this project's label, and volumes declared
// `external: true` are already excluded from the removal list (they belong to
// whoever created them).
//
// The plan is computed first and executed second so the caller can show it and
// ask, or show it and stop (--dry-run). What the user approves is exactly what
// runs: the plan is the single source of truth for both, rather than a
// description of the removal written alongside it and free to drift.

// DestroyPlan is everything a destroy would remove, in the order it happens.
type DestroyPlan struct {
	// SupervisorRunning is true when a restart supervisor is watching this
	// project. It is stopped first, before anything it might try to bring back.
	SupervisorRunning bool
	Containers        []string // service containers, `run` leftovers, and orphans
	Networks          []string // the project network plus any it declared (never external ones)
	Volumes           []string // named and anonymous volumes (never external ones)
	Images            []string // built by opossum and pulled for this project
	Paths             []string // everything on disk that will be removed
	// LocalPaths is the subset of Paths that lives in LocalDir — the files that
	// belong to a *place* rather than to the project name. The supervisor's state
	// directory is keyed by name and lives under the user's state dir, so it is
	// deliberately not here: calling it "this directory's files" was a small lie
	// that also made the mistargeting guard fire when there was nothing local to
	// protect.
	LocalPaths []string

	// KeptOverlay is a compose.opossum.yaml that is being left alone, and why. The
	// overlay is a documented place for people to put their own adjustments, so one
	// opossum did not write is not opossum's to remove — and saying so is better
	// than leaving the user to notice the file is still there.
	KeptOverlay string

	// LocalDir is the directory whose generated files are in Paths, or "" when none
	// are. The runtime objects above belong to a project *name*; these belong to a
	// *place*. Naming it is what stops "destroying project X" from reading as a
	// promise about X's directory when the command was run somewhere else.
	LocalDir string

	// MistargetedName is the project this directory belongs to, when that is not the
	// project being destroyed — `destroy -p other` run inside project `mine`. The
	// runtime objects are `other`'s and removing them is meant; the files are
	// `mine`'s and removing them almost certainly is not.
	//
	// The caller sets it, because only the caller knows whether the name was given
	// on the command line or read from the compose file. A compose file that names
	// its project something other than its directory is ordinary and means nothing
	// is mistargeted; deriving this from the directory alone would flag every such
	// project, which is most of them.
	MistargetedName string

	// StrandedVolumes are volumes labelled for this project that no current service
	// claims — left by a service that was renamed or removed from the compose file.
	// They are listed, not removed: working out which of them is genuinely an
	// orphan needs care, and a teardown that says "everything is gone" while these
	// sit on disk is the more urgent problem.
	StrandedVolumes []string

	// SnapshotDirs are `.opossum-snapshots` directories found here. They are never
	// removed: a snapshot belongs to a *directory*, and the same directory outlives
	// any number of projects — an agent's workspace is not this project's to throw
	// away. But they are often the largest thing opossum leaves on the disk, and a
	// command that promises to leave no trace should not be the reason someone finds
	// them a month later.
	SnapshotDirs []string
}

// Empty reports whether there is nothing to remove, so a caller can say so
// instead of asking a question with no stakes.
func (p DestroyPlan) Empty() bool {
	// StrandedVolumes counts. They are not removed, but they are the one thing a
	// second destroy would otherwise hide behind "nothing left" — which is the
	// sentence this reporting exists to stop being false.
	return !p.SupervisorRunning && len(p.Containers) == 0 && len(p.Networks) == 0 &&
		len(p.Volumes) == 0 && len(p.Images) == 0 && len(p.Paths) == 0 &&
		len(p.StrandedVolumes) == 0
}

// DestroyPlanFor works out what destroying this project would remove. It only
// lists things that are actually there: a plan is shown to a person about to
// approve it, so naming a container that does not exist would teach them to stop
// reading it. keepOverlay leaves compose.opossum.yaml alone — it is generated,
// but a user may have edited it.
func (o *Orchestrator) DestroyPlanFor(keepOverlay, keepImages, keepLocal bool) (DestroyPlan, error) {
	var p DestroyPlan
	// Ask whether the daemon is reachable before asking it anything else. Every
	// existence check below reads a failed query as "not there", so an unreachable
	// runtime would produce an empty plan — and destroy would report "nothing to
	// remove" over a project that is still entirely present. `ps` guards the same
	// way for the same reason: an empty answer has to mean empty, not "couldn't ask".
	if !o.rt.SystemRunning() {
		return p, ErrRuntimeStopped()
	}
	order, err := o.Project.StartupOrder()
	if err != nil {
		return p, err
	}

	p.SupervisorRunning = SupervisorPID(o.Project.Name) != 0

	for _, name := range order {
		for _, cname := range []string{o.containerName(name), o.containerName(name + "-run")} {
			// Ours, by the label — not merely by the name. With `--dns-domain ""` a
			// container is called plain `web`, which anything on the machine could be;
			// a command that promises to remove a project must not remove a namesake.
			if owner, ok := o.rt.InspectLabel(cname, projectLabel); ok && owner == o.Project.Name {
				p.Containers = append(p.Containers, cname)
			}
		}
	}
	// Orphans are containers labelled with THIS project that no current service
	// claims — a service that was renamed or deleted from the compose file. The
	// label is what keeps the sweep inside the project.
	p.Containers = append(p.Containers, o.orphans()...)
	sort.Strings(p.Containers)

	for _, net := range append([]string{o.networkName()}, o.declaredNetworks()...) {
		if o.rt.NetworkExists(net) {
			p.Networks = append(p.Networks, net)
		}
	}
	sort.Strings(p.Networks)

	// namedVolumes already leaves out bind mounts and external volumes.
	for _, vol := range o.namedVolumes() {
		if o.rt.VolumeExists(vol) {
			p.Volumes = append(p.Volumes, vol)
		}
	}

	// Images are the one group that can outlive the project usefully: a pulled
	// `postgres:16` may be shared with three other projects and cost minutes to
	// fetch again. Removing them is still the default — "destroy" would be a strange
	// name for something that left gigabytes behind — but there is a way to say no
	// short of abandoning the whole command.
	if !keepImages {
		seen := map[string]bool{}
		for _, name := range order {
			ref, _ := o.serviceImage(name, o.Project.Services[name])
			if ref == "" || seen[ref] {
				continue
			}
			seen[ref] = true
			if o.rt.ImageExists(ref) {
				p.Images = append(p.Images, ref)
			}
		}
		sort.Strings(p.Images)
	}

	local, byName, kept := o.destroyPaths(keepOverlay, keepLocal)
	p.KeptOverlay = kept
	if o.Project.BaseDir != "" {
		p.LocalDir = o.Project.BaseDir
	}
	p.StrandedVolumes = o.strandedVolumes(p.Volumes)
	for _, path := range local {
		if _, err := os.Stat(path); err == nil {
			p.LocalPaths = append(p.LocalPaths, path)
			p.Paths = append(p.Paths, path)
		}
	}
	for _, path := range byName {
		if _, err := os.Stat(path); err == nil {
			p.Paths = append(p.Paths, path)
		}
	}
	// Last, once the removal list is settled: a snapshot directory that sits inside
	// something being removed is not being left alone, and saying it is — with an
	// `rm -rf` for a path that will not exist — is the worst version of this report.
	// A workspace under `.opossum/` puts its snapshots there, and `.opossum/` goes.
	p.SnapshotDirs = outside(o.snapshotDirs(), p.Paths)
	return p, nil
}

// outside drops the directories that lie inside something being removed.
func outside(dirs, removed []string) []string {
	var kept []string
	for _, dir := range dirs {
		inside := false
		for _, path := range removed {
			if dir == path || strings.HasPrefix(dir, path+string(filepath.Separator)) {
				inside = true
				break
			}
		}
		if !inside {
			kept = append(kept, dir)
		}
	}
	return kept
}

// snapshotDirs finds the workspace snapshot directories under this project's
// directory. `ws` puts them beside the workspace, so the default `./work` leaves
// one here, and a workspace a level down leaves one in that subdirectory.
//
// A workspace given as `.` puts its snapshots in the *parent*, which is shared
// with every sibling directory; that one is deliberately not looked for, because
// naming a directory outside the project as something destroy considered is worse
// than not mentioning it.
func (o *Orchestrator) snapshotDirs() []string {
	base := o.Project.BaseDir
	if base == "" {
		base = "."
	}
	var found []string
	add := func(dir string) {
		p := filepath.Join(dir, workspace.SnapshotDirName)
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			found = append(found, p)
		}
	}
	add(base)
	entries, err := os.ReadDir(base)
	if err != nil {
		return found
	}
	for _, e := range entries {
		if e.IsDir() && e.Name() != workspace.SnapshotDirName {
			add(filepath.Join(base, e.Name()))
		}
	}
	sort.Strings(found)
	return found
}

// strandedVolumes finds volumes named for this project that the compose file no
// longer accounts for: `<project>_<name>` left behind by a service that was
// renamed or deleted. destroy can't remove them safely — a volume declared
// `external: true` can carry the same prefix, and there is no label to tell them
// apart — but it can stop pretending they aren't there. Before this, a teardown
// reported that everything opossum made was gone while these stayed on the disk,
// invisible.
func (o *Orchestrator) strandedVolumes(planned []string) []string {
	inPlan := map[string]bool{}
	for _, v := range planned {
		inPlan[v] = true
	}
	// External volumes are someone else's whatever they are called, so a name this
	// project declares as external is never stranded.
	external := map[string]bool{}
	for key, decl := range o.Project.Volumes {
		if decl.External {
			external[o.externalRealName(key)] = true
		}
	}
	prefix := o.Project.Name + "_"
	var out []string
	for _, name := range o.rt.ListVolumes() {
		if !strings.HasPrefix(name, prefix) || inPlan[name] || external[name] {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// declaredNetworks names the networks this project declared and therefore owns.
// External ones are declared but belong to whoever created them.
func (o *Orchestrator) declaredNetworks() []string {
	var out []string
	for key, decl := range o.Project.Networks {
		if decl.External {
			continue
		}
		out = append(out, o.Project.Name+"-"+key)
	}
	return out
}

// destroyPaths lists the on-disk state opossum owns for this project, and
// separately any overlay it is leaving alone. Every entry in the first list is
// something opossum wrote; nothing there is a file the user authored.
func (o *Orchestrator) destroyPaths(keepOverlay, keepLocal bool) (local, byName []string, keptOverlay string) {
	// keepLocal is for destroying a project by name from somewhere else: the runtime
	// objects belong to the name, these files belong to the directory, and the two
	// are not the same thing.
	if o.Project.BaseDir != "" && !keepLocal {
		local = append(local, filepath.Join(o.Project.BaseDir, ".opossum"))
		if overlay := compose.DiscoverOpossumOverlay(o.Project.BaseDir); overlay != "" {
			switch {
			case !generatedOverlay(overlay):
				// Checked before --keep-overlay so the reason is the true one: this
				// file was never destroy's to remove, flag or no flag.
				keptOverlay = overlay + " (not generated by opossum — yours to keep or delete)"
			case keepOverlay:
				keptOverlay = overlay + " (--keep-overlay)"
			default:
				local = append(local, overlay)
			}
		}
	}
	// The supervisor's own directory: pid file, watch record, and its log. Removing
	// it is the difference between "the process is gone" and "no trace of it".
	if dir, err := supervisorStateDir(o.Project.Name); err == nil {
		byName = append(byName, dir)
	}
	return local, byName, keptOverlay
}

// generatedOverlay reports whether this overlay file starts with the header
// opossum writes. Reading one line is enough: the header is the first thing in
// every file opossum generates, and its absence is what matters.
func generatedOverlay(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false // unreadable: treat as not ours, which is the cautious way round
	}
	defer f.Close()
	head := make([]byte, len(overlayGeneratedHeader))
	n, _ := io.ReadFull(f, head)
	return string(head[:n]) == overlayGeneratedHeader
}

// Destroy carries out a plan from DestroyPlanFor, reporting each removal as it
// goes so the record on screen is of what happened rather than what was intended.
// It executes the plan it is given and works nothing out for itself — a caller
// that showed the user a plan has therefore shown them the whole of it.
//
// Removal is best-effort per item, as in Down: a volume still held by a
// container elsewhere, or a file already gone, must not stop the rest of the
// teardown. The error is reserved for a failure that leaves the user's own files
// in doubt, which is why only path removal can produce one.
func (o *Orchestrator) Destroy(p DestroyPlan) error {
	if p.SupervisorRunning {
		if StopSupervisor(o.Project.Name) {
			o.logf("Stopped the restart supervisor\n")
		}
	}

	for _, cname := range p.Containers {
		o.logf("Removing container %s\n", cname)
		o.rt.Stop(cname)
		o.rt.Delete(cname)
	}
	for _, net := range p.Networks {
		o.logf("Removing network %s\n", net)
		o.rt.DeleteNetwork(net)
	}
	for _, vol := range p.Volumes {
		o.logf("Removing volume %s\n", vol)
		o.rt.DeleteVolume(vol)
	}
	for _, img := range p.Images {
		o.logf("Removing image %s\n", img)
		o.rt.DeleteImage(img)
	}
	var failed []string
	for _, path := range p.Paths {
		o.logf("Removing %s\n", path)
		if err := os.RemoveAll(path); err != nil {
			failed = append(failed, fmt.Sprintf("%s (%v)", path, err))
		}
	}
	// Look again. Every runtime removal here is best-effort and silent — a volume
	// still held elsewhere, a network with a stray endpoint — so without this the
	// command would report a clean teardown on the strength of having asked for one.
	failed = append(failed, o.survivors(p)...)
	if len(failed) > 0 {
		return fmt.Errorf("%d thing(s) could not be removed: %s — "+
			"run `opossum destroy` again (a container elsewhere may still be holding one), "+
			"or remove them by hand", len(failed), joinAnd(failed))
	}
	return nil
}

// survivors re-checks what the plan said it would remove and names whatever is
// still there.
func (o *Orchestrator) survivors(p DestroyPlan) []string {
	var left []string
	for _, cname := range p.Containers {
		if o.rt.Inspect(cname).Exists {
			left = append(left, "container "+cname)
		}
	}
	for _, net := range p.Networks {
		if o.rt.NetworkExists(net) {
			left = append(left, "network "+net)
		}
	}
	for _, vol := range p.Volumes {
		// "Couldn't ask" is not "still there": VolumeExists answers true when the
		// listing fails (right for deciding whether to seed, wrong here — it would
		// report a survivor after a removal that worked).
		if exists, known := o.rt.VolumeListed(vol); known && exists {
			left = append(left, "volume "+vol)
		}
	}
	for _, img := range p.Images {
		if o.rt.ImageExists(img) {
			left = append(left, "image "+img)
		}
	}
	return left
}

// joinAnd renders a short list for a sentence.
func joinAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	out := items[0]
	for _, s := range items[1 : len(items)-1] {
		out += ", " + s
	}
	return out + " and " + items[len(items)-1]
}
