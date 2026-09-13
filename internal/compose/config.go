package compose

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// configOutput mirrors the compose schema for rendering the *resolved* project
// (interpolation done, env_file folded in) as canonical YAML — like
// `docker compose config`.
type configOutput struct {
	Name     string                   `yaml:"name,omitempty"`
	Services map[string]configService `yaml:"services"`
	Networks map[string]configNetwork `yaml:"networks,omitempty"`
	// The declarations that change what a run means — a volume's `external`
	// and `name`, a secret's or config's source — travel with the output, or
	// the output run back would make an external volume a namespaced, seeded
	// one of its own and drop every secret and config mount (#939).
	Volumes map[string]configVolume `yaml:"volumes,omitempty"`
	Secrets map[string]configSecret `yaml:"secrets,omitempty"`
	Configs map[string]configConfig `yaml:"configs,omitempty"`
}

// configVolume carries what `up` acts on: `external`, and the `name:` a
// declaration gives — the real name of an external volume, or the name a
// project volume is created under instead of `<project>_<key>` (#937). Both
// are read by `up`, so the output says exactly what the run does.
type configVolume struct {
	External bool   `yaml:"external,omitempty"`
	Name     string `yaml:"name,omitempty"`
}

type configSecret struct {
	File     string `yaml:"file,omitempty"`
	External bool   `yaml:"external,omitempty"`
}

// configConfig carries the one form the declaration used: `file`, `content`
// or `environment` (the variable's name — its value is read when the output
// is loaded, as it was for the input).
type configConfig struct {
	File        string  `yaml:"file,omitempty"`
	Content     *string `yaml:"content,omitempty"`
	Environment string  `yaml:"environment,omitempty"`
	External    bool    `yaml:"external,omitempty"`
}

// configRef is a service's reference to a secret or config. The short form is
// written back short (the target it implies is the loader's default again);
// a target of the person's own is written in the long form.
type configRef struct {
	Source string `yaml:"source"`
	Target string `yaml:"target,omitempty"`
}

type configNetwork struct {
	Internal bool              `yaml:"internal,omitempty"`
	External bool              `yaml:"external,omitempty"`
	Name     string            `yaml:"name,omitempty"`
	Labels   map[string]string `yaml:"labels,omitempty"`
	IPAM     *configIPAM       `yaml:"ipam,omitempty"`
}

// configIPAM renders the subnets opossum read, in docker compose's shape
// (`ipam: {config: [{subnet: …}]}`), one entry per subnet.
type configIPAM struct {
	Config []map[string]string `yaml:"config"`
}

func ipamConfig(i IPAM) *configIPAM {
	var entries []map[string]string
	for _, sub := range []string{i.Subnet, i.SubnetV6} {
		if sub != "" {
			entries = append(entries, map[string]string{"subnet": sub})
		}
	}
	if entries == nil {
		return nil
	}
	return &configIPAM{Config: entries}
}

// ulimitMap renders the limits as docker compose shows them: a number when
// soft and hard match, `{soft, hard}` otherwise.
func ulimitMap(u Ulimits) map[string]any {
	if len(u) == 0 {
		return nil
	}
	m := make(map[string]any, len(u))
	for n, l := range u {
		if l.Soft == l.Hard {
			m[n] = l.Soft
		} else {
			m[n] = map[string]int64{"soft": l.Soft, "hard": l.Hard}
		}
	}
	return m
}

// labelMap renders `key=value` labels as the mapping docker compose shows.
func labelMap(labels []string) map[string]string {
	if len(labels) == 0 {
		return nil
	}
	m := make(map[string]string, len(labels))
	for _, l := range labels {
		k, v, _ := strings.Cut(l, "=")
		m[k] = v
	}
	return m
}

type configService struct {
	Image       string               `yaml:"image,omitempty"`
	Platform    string               `yaml:"platform,omitempty"`
	Build       *configBuild         `yaml:"build,omitempty"`
	Command     []string             `yaml:"command,omitempty"`
	Entrypoint  []string             `yaml:"entrypoint,omitempty"`
	Environment []string             `yaml:"environment,omitempty"`
	Ports       []string             `yaml:"ports,omitempty"`
	Restart     string               `yaml:"restart,omitempty"`
	Volumes     []string             `yaml:"volumes,omitempty"`
	Tmpfs       []string             `yaml:"tmpfs,omitempty"`
	MemLimit    string               `yaml:"mem_limit,omitempty"`
	CPUs        string               `yaml:"cpus,omitempty"`
	SSH         bool                 `yaml:"ssh,omitempty"`
	User        string               `yaml:"user,omitempty"`
	WorkingDir  string               `yaml:"working_dir,omitempty"`
	Init        bool                 `yaml:"init,omitempty"`
	ShmSize     string               `yaml:"shm_size,omitempty"`
	Ulimits     map[string]any       `yaml:"ulimits,omitempty"`
	ReadOnly    bool                 `yaml:"read_only,omitempty"`
	CapAdd      []string             `yaml:"cap_add,omitempty"`
	CapDrop     []string             `yaml:"cap_drop,omitempty"`
	NetworkMode string               `yaml:"network_mode,omitempty"`
	MacAddress  string               `yaml:"mac_address,omitempty"`
	Labels      map[string]string    `yaml:"labels,omitempty"`
	Networks    []string             `yaml:"networks,omitempty"`
	Secrets     []any                `yaml:"secrets,omitempty"`
	Configs     []any                `yaml:"configs,omitempty"`
	DependsOn   map[string]configDep `yaml:"depends_on,omitempty"`
	Healthcheck *configHealthcheck   `yaml:"healthcheck,omitempty"`
}

type configBuild struct {
	Context    string   `yaml:"context,omitempty"`
	Dockerfile string   `yaml:"dockerfile,omitempty"`
	Args       []string `yaml:"args,omitempty"`
	Target     string   `yaml:"target,omitempty"`
}

type configDep struct {
	Condition string `yaml:"condition"`
}

type configHealthcheck struct {
	Test        []string `yaml:"test,omitempty"`
	Interval    string   `yaml:"interval,omitempty"`
	Timeout     string   `yaml:"timeout,omitempty"`
	Retries     int      `yaml:"retries,omitempty"`
	StartPeriod string   `yaml:"start_period,omitempty"`
	Disabled    bool     `yaml:"disable,omitempty"`
}

// RenderConfig returns the resolved project as canonical compose YAML. Fields
// opossum parses but doesn't act on are appended as a trailing comment so the
// YAML body stays valid.
func RenderConfig(p *Project) (string, error) {
	out := configOutput{Name: p.Name, Services: map[string]configService{}}
	for name, svc := range p.Services {
		mem, cpu, _ := svc.Resources() // validated at load; show the effective -m/-c
		// The rendered output carries the environment, so this is one of the
		// places an env_file failure has been waiting for.
		env, err := svc.ResolvedEnv()
		if err != nil {
			return "", err
		}
		// Shown resolved, as docker compose shows them: a bare `NAME` takes
		// the shell's value; unset it stays bare in `environment` (the
		// runtime is still told the name) and is left out of `build.args`
		// (what `up --build` passes).
		cs := configService{
			Image:       svc.Image,
			Platform:    svc.Platform,
			Command:     svc.Command,
			Entrypoint:  svc.Entrypoint,
			Environment: ResolveBareNames(env, os.LookupEnv, true),
			Ports:       svc.Ports,
			Restart:     svc.Restart,
			Volumes:     volumesWithNoCopy(svc),
			Tmpfs:       svc.Tmpfs,
			MemLimit:    mem,
			CPUs:        cpu,
			SSH:         svc.SSH,
			User:        svc.User,
			WorkingDir:  svc.WorkingDir,
			Init:        svc.Init,
			ShmSize:     string(svc.ShmSize),
			Ulimits:     ulimitMap(svc.Ulimits),
			ReadOnly:    svc.ReadOnly,
			CapAdd:      svc.CapAdd,
			CapDrop:     svc.CapDrop,
			NetworkMode: svc.NetworkMode,
			MacAddress:  svc.MacAddress,
			Labels:      labelMap(svc.Labels),
			Networks:    svc.Networks,
			Secrets:     secretRefs(svc.Secrets),
			Configs:     configRefs(svc.Configs),
		}
		if svc.Build != nil {
			cs.Build = &configBuild{Context: svc.Build.Context, Dockerfile: svc.Build.Dockerfile, Args: ResolveBareNames(svc.Build.Args, os.LookupEnv, false), Target: svc.Build.Target}
		}
		if len(svc.DependsOn) > 0 {
			cs.DependsOn = map[string]configDep{}
			for _, dep := range svc.DependsOn {
				cs.DependsOn[dep.Name] = configDep{Condition: dep.Condition}
			}
		}
		if hc := svc.Healthcheck; hc != nil {
			cs.Healthcheck = &configHealthcheck{
				Test:     hc.Test,
				Retries:  hc.Retries,
				Disabled: hc.Disabled,
			}
			if hc.Interval > 0 {
				cs.Healthcheck.Interval = hc.Interval.String()
			}
			if hc.Timeout > 0 {
				cs.Healthcheck.Timeout = hc.Timeout.String()
			}
			if hc.StartPeriod > 0 {
				cs.Healthcheck.StartPeriod = hc.StartPeriod.String()
			}
		}
		out.Services[name] = cs
	}
	if len(p.Networks) > 0 {
		out.Networks = map[string]configNetwork{}
		for name, decl := range p.Networks {
			out.Networks[name] = configNetwork{Internal: decl.Internal, External: decl.External, Name: decl.Name, Labels: labelMap(decl.Labels), IPAM: ipamConfig(decl.IPAM)}
		}
	}

	if len(p.Volumes) > 0 {
		out.Volumes = map[string]configVolume{}
		for name, decl := range p.Volumes {
			out.Volumes[name] = configVolume{External: decl.External, Name: decl.Name}
		}
	}
	if len(p.Secrets) > 0 {
		out.Secrets = map[string]configSecret{}
		for name, decl := range p.Secrets {
			out.Secrets[name] = configSecret{File: decl.File, External: decl.External}
		}
	}
	if len(p.Configs) > 0 {
		out.Configs = map[string]configConfig{}
		for name, decl := range p.Configs {
			out.Configs[name] = configConfig{File: decl.File, Content: decl.Content, Environment: decl.EnvVar, External: decl.External}
		}
	}

	body, err := yaml.Marshal(out)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	// Every `$` in the values came through interpolation, where `$$` reads as
	// one `$`; written back bare it would be read as a variable reference the
	// next time this output is loaded — a healthcheck's `$${PG_USER}`, meant
	// for the container's shell, expanding to nothing on the host, or a
	// `$$HOME` turning into this machine's path. So `$` goes out as `$$`, the
	// way docker compose config prints it, and the output reads back to the
	// same values (a literal `$$`, written `$$$$`, comes out as `$$$$` again).
	// The whole output is escaped rather than field by field: nothing
	// opossum writes here carries a `$` of its own, and a field-by-field list
	// is one more place for a new field to be missed. The trailing comments
	// are included — the loader interpolates comments too, and they carry
	// declaration names the person wrote.
	b.Write(body)
	if ignored := ignoredComment(p); ignored != "" {
		b.WriteString(ignored)
	}
	if caveat := restartCaveat(p); caveat != "" {
		b.WriteString(caveat)
	}
	return strings.ReplaceAll(b.String(), "$", "$$"), nil
}

// ignoredComment lists, as YAML comments, the fields opossum ignores — both
// top-level and per service.
func ignoredComment(p *Project) string {
	names := make([]string, 0, len(p.Services))
	for name, svc := range p.Services {
		if len(svc.Unsupported) > 0 {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(p.Unsupported) == 0 && len(names) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n# fields opossum ignores (parsed but not acted on):\n")
	if len(p.Unsupported) > 0 {
		fmt.Fprintf(&b, "#   (top-level): %s\n", strings.Join(p.Unsupported, ", "))
	}
	for _, name := range names {
		fmt.Fprintf(&b, "#   %s: %s\n", name, strings.Join(p.Services[name].Unsupported, ", "))
	}
	return b.String()
}

// restartCaveat warns, as a YAML comment, about the one restart policy opossum
// cannot honour exactly. `on-failure` means "restart only if it failed", but Apple
// `container` doesn't report a container's exit code, so a crash and a clean exit
// are indistinguishable from outside. opossum retries a bounded number of times
// rather than looping a service that may have finished on purpose — and says so
// here, because a user reading their resolved config is exactly who needs to know
// their policy is being approximated.
func restartCaveat(p *Project) string {
	var names []string
	for name, svc := range p.Services {
		if pol, err := svc.RestartPolicy(); err == nil && pol.Mode == RestartOnFailure {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	verb := "uses"
	if len(names) > 1 {
		verb = "use"
	}
	return fmt.Sprintf("\n# note: %s %s `restart: on-failure`, which opossum can only approximate —\n"+
		"#   Apple container does not report a container's exit code, so a crash and a clean\n"+
		"#   exit look the same. opossum retries a few times and then stops, rather than\n"+
		"#   restarting a service that may have finished on purpose. `always` and\n"+
		"#   `unless-stopped` are honoured exactly.\n", strings.Join(names, ", "), verb)
}

// volumesWithNoCopy puts the `nocopy` option back on the mounts that carry it.
// Parsing lifts it off the string into Service.NoCopy, and `config` is meant to
// print a compose file you could run — so a mount whose seeding was switched off
// has to say so, or feeding the output back in would turn the copy on again.
// Rendered in the short spelling, which is what the long form means.
// secretRefs writes a service's secret references back: the short form for a
// reference the loader read from a bare name (its target is the name), the
// long form when a target was given.
func secretRefs(refs SecretRefs) []any {
	if len(refs) == 0 {
		return nil
	}
	out := make([]any, 0, len(refs))
	for _, r := range refs {
		if r.Target == r.Source {
			out = append(out, r.Source)
			continue
		}
		out = append(out, configRef{Source: r.Source, Target: r.Target})
	}
	return out
}

// configRefs is secretRefs for configs, whose bare-name target is `/<name>`.
func configRefs(refs ConfigRefs) []any {
	if len(refs) == 0 {
		return nil
	}
	out := make([]any, 0, len(refs))
	for _, r := range refs {
		if r.Target == "/"+r.Source {
			out = append(out, r.Source)
			continue
		}
		out = append(out, configRef{Source: r.Source, Target: r.Target})
	}
	return out
}

func volumesWithNoCopy(svc *Service) []string {
	if len(svc.NoCopy) == 0 {
		return svc.Volumes
	}
	off := map[string]bool{}
	for _, t := range svc.NoCopy {
		off[strings.TrimRight(t, "/")] = true
	}
	out := make([]string, 0, len(svc.Volumes))
	for _, v := range svc.Volumes {
		if !off[mountTarget(v)] {
			out = append(out, v)
			continue
		}
		// An anonymous volume is written as the target alone: there is no source, so
		// there is no mode field to hang the option on and no short spelling that
		// says this. Left as it is, which loses the option — documented, not silent.
		if !strings.Contains(v, ":") {
			out = append(out, v)
			continue
		}
		// The mode field is comma-separated, and a mount may already have one.
		// Appending with another colon produces `deps:/app:ro:nocopy`, which is not
		// a mount spec at all: the runtime takes it as written and the option is
		// unreadable on the way back in.
		if strings.Count(v, ":") >= 2 {
			out = append(out, v+",nocopy")
			continue
		}
		out = append(out, v+":nocopy")
	}
	return out
}
