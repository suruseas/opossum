package orchestrator

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"
)

// HostFootprinter maps a running container's name to the host memory its
// per-container VM actually occupies (resident bytes). This is the one thing
// Docker Desktop and the other single-VM tools structurally can't report per
// service — on Apple `container` each container is its own VM, so its cost to the
// Mac is a real, separable number.
//
// It is deliberately isolated behind this interface: reading it today requires
// host-process introspection (the `container` CLI doesn't expose a VM PID or host
// memory), which is best-effort and tied to Apple's process layout. Keeping it
// here means the one fragile place is swappable for a `container` API the moment
// Apple exposes one, without touching callers. A container it can't map is simply
// absent from the result (callers render that as unknown) — Footprints never
// fails the command.
type HostFootprinter interface {
	Footprints() map[string]int64
}

// vmProcessName is the process each container's VM runs as: an instance of the
// Virtualization framework's VM XPC service. opossum matches on it to enumerate
// the VMs, then maps each to a container by the container's data files it holds
// open (see Footprints).
const vmProcessName = "com.apple.Virtualization.VirtualMachine"

// containerPathRe extracts a container name from a file path the runtime opens
// per container: .../com.apple.container/containers/<name>/...
var containerPathRe = regexp.MustCompile(`/com\.apple\.container/containers/([^/]+)/`)

// vmFootprinter reads host footprints via process introspection on macOS:
// enumerate the per-container VM processes, map each to its container by the
// container-data files it holds open (lsof), and read that process's resident
// size (ps). Resident size approximates the VM's physical footprint (verified
// against Activity Monitor). Every step is best-effort: any failure (not on
// macOS, tools missing, nothing running) yields an empty map, never an error.
type vmFootprinter struct{}

func (vmFootprinter) Footprints() map[string]int64 {
	out := map[string]int64{}
	pids := vmPIDs()
	if len(pids) == 0 {
		return out
	}
	rss := pidRSS(pids)          // pid -> resident bytes
	names := pidContainers(pids) // pid -> container name
	for pid, name := range names {
		if bytes, ok := rss[pid]; ok {
			out[name] = bytes
		}
	}
	return out
}

// vmPIDs lists the PIDs of the per-container VM processes.
func vmPIDs() []string {
	out, err := exec.Command("pgrep", "-f", vmProcessName).Output()
	if err != nil {
		return nil // exit 1 = no matches, or pgrep unavailable — both mean "none"
	}
	return strings.Fields(string(out))
}

// pidRSS reads each PID's resident set size (KiB from ps) as bytes.
func pidRSS(pids []string) map[string]int64 {
	res := map[string]int64{}
	args := append([]string{"-o", "pid=,rss="}, pidFlags(pids)...)
	out, err := exec.Command("ps", args...).Output()
	if err != nil {
		return res
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		kib, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil {
			continue
		}
		res[f[0]] = kib * 1024
	}
	return res
}

// pidContainers maps each VM PID to the container it hosts, found via the
// container-data files it holds open (lsof, field output: `p<pid>` then `n<path>`).
func pidContainers(pids []string) map[string]string {
	res := map[string]string{}
	args := append([]string{"-Fpn"}, pidFlags(pids)...)
	out, err := exec.Command("lsof", args...).Output()
	if err != nil && len(out) == 0 {
		return res
	}
	var cur string
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			cur = line[1:]
		case 'n':
			if _, seen := res[cur]; seen {
				continue
			}
			if m := containerPathRe.FindStringSubmatch(line[1:]); m != nil {
				res[cur] = m[1]
			}
		}
	}
	return res
}

// pidFlags turns pids into the ["-p", "1,2,3"] form ps and lsof both accept.
func pidFlags(pids []string) []string {
	return []string{"-p", strings.Join(pids, ",")}
}

// StatsHost prints a one-shot table of each service's guest-view memory usage
// alongside the host memory its VM actually occupies — the per-service cost to
// the Mac. The host column is host-derived and approximate (see HostFootprinter);
// a service whose VM can't be mapped shows "—" rather than failing the command.
func (o *Orchestrator) StatsHost(services []string) error {
	targets, err := o.resolveServices(services)
	if err != nil {
		return err
	}
	names := make([]string, len(targets))
	for i, name := range targets {
		names[i] = o.containerName(name)
	}

	// Guest-view usage (best-effort — a container that isn't running just has no
	// row, which we render as "—" too).
	guest := map[string]runtimeStat{}
	if snap, err := o.rt.StatsSnapshot(names); err == nil {
		for _, s := range snap {
			guest[s.ID] = runtimeStat{usage: s.MemoryUsageBytes, limit: s.MemoryLimitBytes}
		}
	}

	fp := o.hostFootprinter()
	foot := fp.Footprints()

	w := tabwriter.NewWriter(o.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SERVICE\tGUEST MEM\tHOST FOOTPRINT")
	var total int64
	var mapped bool
	for i, svc := range targets {
		cname := names[i]
		gm := "—"
		if g, ok := guest[cname]; ok {
			gm = humanBytes(g.usage) + " / " + humanBytes(g.limit)
		}
		hm := "—"
		if b, ok := foot[cname]; ok {
			hm = humanBytes(b)
			total += b
			mapped = true
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", svc, gm, hm)
	}
	if mapped {
		fmt.Fprintf(w, "\ttotal\t%s\n", humanBytes(total))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(o.out, "\nHOST FOOTPRINT is each container VM's resident memory on the Mac (host-derived, approximate); — means it couldn't be mapped.")
	return nil
}

type runtimeStat struct{ usage, limit int64 }

// hostFootprinter returns the injected footprinter, or the real macOS one.
func (o *Orchestrator) hostFootprinter() HostFootprinter {
	if o.HostFP != nil {
		return o.HostFP
	}
	return vmFootprinter{}
}

// humanBytes formats a byte count with a binary unit, dropping a trailing ".0".
func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	v := float64(b) / float64(div)
	s := strconv.FormatFloat(v, 'f', 1, 64)
	s = strings.TrimSuffix(s, ".0")
	return s + string("KMGTPE"[exp]) + "iB"
}
