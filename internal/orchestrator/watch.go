package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// watchTarget is a resolved develop.watch rule: an absolute host path to watch,
// and what to do when files under it change.
type watchTarget struct {
	service string
	action  string
	hostDir string // absolute host path (a directory or a single file)
	target  string // container path files sync to
	ignore  []string
}

// watchTargets resolves every service's develop.watch rules into absolute host
// paths, in startup order. Rules with no action default to "sync".
func (o *Orchestrator) watchTargets() []watchTarget {
	var ts []watchTarget
	order, _ := o.Project.StartupOrder()
	for _, name := range order {
		svc := o.Project.Services[name]
		if svc == nil || svc.Develop == nil {
			continue
		}
		for _, w := range svc.Develop.Watch {
			p := w.Path
			if !filepath.IsAbs(p) {
				p = filepath.Join(o.Project.BaseDir, p)
			}
			action := w.Action
			if action == "" {
				action = "sync"
			}
			ts = append(ts, watchTarget{name, action, filepath.Clean(p), w.Target, w.Ignore})
		}
	}
	return ts
}

// match reports whether changed (an absolute host path) falls under this
// target's watched path, and if so the path relative to it (".": the watched
// path is itself the changed file). Ignored paths don't match.
func (t watchTarget) match(changed string) (rel string, ok bool) {
	rel, err := filepath.Rel(t.hostDir, changed)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	if ignored(rel, t.ignore) {
		return "", false
	}
	return rel, true
}

// ignored reports whether rel (a slash- or OS-separated path relative to the
// watched root) matches any ignore glob. A bare directory name ignores its whole
// subtree; a glob is matched against the full relative path and its basename.
func ignored(rel string, patterns []string) bool {
	rel = filepath.ToSlash(rel)
	for _, pat := range patterns {
		pat = strings.TrimSuffix(filepath.ToSlash(pat), "/")
		if pat == "" {
			continue
		}
		if rel == pat || strings.HasPrefix(rel, pat+"/") {
			return true
		}
		if ok, _ := path.Match(pat, rel); ok {
			return true
		}
		if ok, _ := path.Match(pat, path.Base(rel)); ok {
			return true
		}
	}
	return false
}

// containerTarget maps a changed host path to its destination inside the
// container: the rule's target joined with the path relative to the watched root
// ("." meaning the watched path is a single file mapped straight to target).
func (t watchTarget) containerTarget(rel string) string {
	if rel == "." {
		return t.target
	}
	return path.Join(t.target, filepath.ToSlash(rel))
}

// handleChange dispatches a changed host path to the first matching watch rule
// (so if two services watch the same path, only the first in startup order
// acts). It copies the file for `sync` and `sync+restart` immediately, and
// returns a follow-up the caller batches across a burst of edits: kind
// "rebuild" (rebuild the image + recreate) or "restart" (restart the container
// after the sync); kind "" means nothing further. matched reports whether any
// rule applied.
func (o *Orchestrator) handleChange(changed string) (svc, kind string, matched bool) {
	for _, t := range o.watchTargets() {
		rel, ok := t.match(changed)
		if !ok {
			continue
		}
		switch t.action {
		case "sync":
			o.syncFile(t, changed, rel)
		case "sync+restart":
			o.syncFile(t, changed, rel)
			return t.service, "restart", true
		case "rebuild":
			return t.service, "rebuild", true
		default:
			fmt.Fprintf(o.out, "watch: %q change needs action %q for %s, which isn't automated yet — re-run `opossum up --build`\n",
				changed, t.action, t.service)
		}
		return "", "", true
	}
	return "", "", false
}

// applyChanges dispatches a batch of changed paths: `sync` copies run per file
// (inside handleChange), while the heavier follow-ups are de-duplicated so a
// burst of edits triggers just one rebuild/restart per service. A service that's
// rebuilt (which recreates its container) skips a same-batch restart.
func (o *Orchestrator) applyChanges(paths []string) {
	rebuilds, restarts := map[string]struct{}{}, map[string]struct{}{}
	for _, p := range paths {
		svc, kind, _ := o.handleChange(p)
		switch kind {
		case "rebuild":
			rebuilds[svc] = struct{}{}
		case "restart":
			restarts[svc] = struct{}{}
		}
	}
	for svc := range rebuilds {
		fmt.Fprintf(o.out, "rebuilding %s…\n", svc)
		if err := o.rebuildService(svc); err != nil {
			o.warnf(codeWatchRebuild, "rebuild %s failed: %v\n", svc, err)
		}
	}
	for svc := range restarts {
		if _, alsoRebuilt := rebuilds[svc]; alsoRebuilt {
			continue // a rebuild already recreated it
		}
		if err := o.Restart([]string{svc}); err != nil {
			o.warnf(codeWatchRestart, "restart %s failed: %v\n", svc, err)
		}
	}
}

// syncFile copies a changed host file into the container at its mapped target.
func (o *Orchestrator) syncFile(t watchTarget, changed, rel string) {
	dst := t.service + ":" + t.containerTarget(rel)
	fmt.Fprintf(o.out, "sync %s → %s\n", changed, dst)
	if err := o.Copy(changed, dst); err != nil {
		o.warnf(codeWatchSync, "sync failed: %v\n", err)
	}
}

// rebuildService rebuilds a service's image and recreates its container, reusing
// the `up` path scoped to that one service: build + force-recreate, and noDeps
// so a dependency (already running) is never rebuilt or recreated. The `up`
// options are restored afterwards so a long-running watch stays stateless.
func (o *Orchestrator) rebuildService(name string) error {
	prev := o.up
	o.up = upOptions{forceRecreate: true, build: true, noDeps: true}
	defer func() { o.up = prev }()
	return o.Up(true, name)
}

// Watch mirrors host file changes into running containers per each service's
// develop.watch rules, until ctx is cancelled. Directory trees are watched
// recursively; changes are debounced so a burst of writes (e.g. an editor's
// save) triggers one sync per file.
func (o *Orchestrator) Watch(ctx context.Context) error {
	targets := o.watchTargets()
	if len(targets) == 0 {
		return fmt.Errorf("no develop.watch rules in the compose file")
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()

	// Watch each rule's root and (for directories) every subdirectory, since
	// fsnotify is not recursive. Ignored subtrees (e.g. node_modules) are skipped
	// so a large dependency dir doesn't flood the watcher with events.
	for _, t := range targets {
		if err := addTree(w, t); err != nil {
			o.warnf(codeWatchSetup, "watching %s: %v\n", t.hostDir, err)
		}
		fmt.Fprintf(o.out, "watching %s (%s → %s:%s)\n", t.hostDir, t.action, t.service, t.target)
	}

	pending := map[string]struct{}{}
	var timer <-chan time.Time
	flush := func() {
		paths := make([]string, 0, len(pending))
		for p := range pending {
			paths = append(paths, p)
			delete(pending, p)
		}
		o.applyChanges(paths)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			// A new directory under a watched (non-ignored) tree must itself be
			// watched, since fsnotify isn't recursive.
			if ev.Op&fsnotify.Create != 0 {
				if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
					if t, ok := targetFor(targets, ev.Name); ok {
						_ = addSubtree(w, t, ev.Name)
					}
				}
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create) != 0 {
				if fi, err := os.Stat(ev.Name); err == nil && !fi.IsDir() {
					pending[ev.Name] = struct{}{}
					timer = time.After(100 * time.Millisecond)
				}
			}
		case <-timer:
			flush()
			timer = nil
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			o.warnf(codeWatchError, "watch error: %v\n", err)
		}
	}
}

// addTree adds the target's watched path to the watcher, plus every
// subdirectory when it's a directory (fsnotify watches direct entries only).
// Subtrees the rule ignores are skipped, so a large node_modules doesn't flood
// the watcher.
func addTree(w *fsnotify.Watcher, t watchTarget) error {
	return addSubtree(w, t, t.hostDir)
}

// addSubtree walks root (which must lie within t.hostDir) and watches each
// directory the rule doesn't ignore. Used both for initial setup and to pick up
// a directory created while watching.
func addSubtree(w *fsnotify.Watcher, t watchTarget, root string) error {
	fi, err := os.Stat(root)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return w.Add(root)
	}
	return filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil // skip files and unreadable entries
		}
		if rel, err := filepath.Rel(t.hostDir, p); err == nil && rel != "." && ignored(rel, t.ignore) {
			return filepath.SkipDir
		}
		return w.Add(p)
	})
}

// targetFor returns the first watch rule whose tree contains path, or false.
func targetFor(targets []watchTarget, path string) (watchTarget, bool) {
	for _, t := range targets {
		if _, ok := t.match(path); ok {
			return t, true
		}
	}
	return watchTarget{}, false
}
