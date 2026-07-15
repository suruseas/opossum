package compose

import (
	"fmt"
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
}

type configNetwork struct {
	Internal bool   `yaml:"internal,omitempty"`
	External bool   `yaml:"external,omitempty"`
	Name     string `yaml:"name,omitempty"`
}

type configService struct {
	Image       string               `yaml:"image,omitempty"`
	Platform    string               `yaml:"platform,omitempty"`
	Build       *configBuild         `yaml:"build,omitempty"`
	Command     []string             `yaml:"command,omitempty"`
	Entrypoint  []string             `yaml:"entrypoint,omitempty"`
	Environment []string             `yaml:"environment,omitempty"`
	Ports       []string             `yaml:"ports,omitempty"`
	Volumes     []string             `yaml:"volumes,omitempty"`
	Tmpfs       []string             `yaml:"tmpfs,omitempty"`
	MemLimit    string               `yaml:"mem_limit,omitempty"`
	CPUs        string               `yaml:"cpus,omitempty"`
	SSH         bool                 `yaml:"ssh,omitempty"`
	User        string               `yaml:"user,omitempty"`
	WorkingDir  string               `yaml:"working_dir,omitempty"`
	Init        bool                 `yaml:"init,omitempty"`
	ReadOnly    bool                 `yaml:"read_only,omitempty"`
	CapAdd      []string             `yaml:"cap_add,omitempty"`
	CapDrop     []string             `yaml:"cap_drop,omitempty"`
	NetworkMode string               `yaml:"network_mode,omitempty"`
	Networks    []string             `yaml:"networks,omitempty"`
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
		cs := configService{
			Image:       svc.Image,
			Platform:    svc.Platform,
			Command:     svc.Command,
			Entrypoint:  svc.Entrypoint,
			Environment: svc.Environment,
			Ports:       svc.Ports,
			Volumes:     svc.Volumes,
			Tmpfs:       svc.Tmpfs,
			MemLimit:    mem,
			CPUs:        cpu,
			SSH:         svc.SSH,
			User:        svc.User,
			WorkingDir:  svc.WorkingDir,
			Init:        svc.Init,
			ReadOnly:    svc.ReadOnly,
			CapAdd:      svc.CapAdd,
			CapDrop:     svc.CapDrop,
			NetworkMode: svc.NetworkMode,
			Networks:    svc.Networks,
		}
		if svc.Build != nil {
			cs.Build = &configBuild{Context: svc.Build.Context, Dockerfile: svc.Build.Dockerfile, Args: svc.Build.Args, Target: svc.Build.Target}
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
			out.Networks[name] = configNetwork{Internal: decl.Internal, External: decl.External, Name: decl.Name}
		}
	}

	body, err := yaml.Marshal(out)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.Write(body)
	if ignored := ignoredComment(p); ignored != "" {
		b.WriteString(ignored)
	}
	return b.String(), nil
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
