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
// syncs). For `sync` it copies the file into the container; other actions are
// reported as not-yet-supported (so the user knows to rebuild manually). It
// returns whether any rule matched.
func (o *Orchestrator) handleChange(changed string) bool {
	for _, t := range o.watchTargets() {
		rel, ok := t.match(changed)
		if !ok {
			continue
		}
		switch t.action {
		case "sync":
			dst := t.service + ":" + t.containerTarget(rel)
			fmt.Fprintf(o.out, "sync %s → %s\n", changed, dst)
			if err := o.Copy(changed, dst); err != nil {
				fmt.Fprintf(o.out, "warning: sync failed: %v\n", err)
			}
		default:
			fmt.Fprintf(o.out, "watch: %q change needs action %q for %s, which isn't automated yet — re-run `opossum up --build`\n",
				changed, t.action, t.service)
		}
		return true
	}
	return false
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
			fmt.Fprintf(o.out, "warning: watching %s: %v\n", t.hostDir, err)
		}
		fmt.Fprintf(o.out, "watching %s (%s → %s:%s)\n", t.hostDir, t.action, t.service, t.target)
	}

	pending := map[string]struct{}{}
	var timer <-chan time.Time
	flush := func() {
		for p := range pending {
			o.handleChange(p)
			delete(pending, p)
		}
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
			fmt.Fprintf(o.out, "warning: watch error: %v\n", err)
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
