// Package servicetest provides in-memory Compose and Docker fakes. Wire
// connects them so that a fake `compose up` creates containers whose facts
// mirror the rendered compose file, which lets service tests exercise the
// full deploy pipeline including the runtime check.
package servicetest

import (
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"

	"github.com/sebmahr/deploy-gateway/internal/dockerx"
	"github.com/sebmahr/deploy-gateway/internal/names"
)

type FakeCompose struct {
	mu        sync.Mutex
	calls     []string
	locks     map[string]bool
	UpErr     error
	UpOut     string
	OnUp      func(file, project string) error
	OnDown    func(project string)
	OnStop    func(project, service string)
	OnRestart func(project, service string)
	AfterUp   func()
}

func (f *FakeCompose) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.TrimSpace(s))
}

func (f *FakeCompose) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *FakeCompose) Up(_ context.Context, file, project string) (string, error) {
	f.record("up " + project + " " + file)
	if f.UpErr != nil {
		return f.UpOut, f.UpErr
	}
	if f.OnUp != nil {
		if err := f.OnUp(file, project); err != nil {
			return "", err
		}
	}
	if f.AfterUp != nil {
		f.AfterUp()
	}
	return f.UpOut, nil
}

func (f *FakeCompose) Down(_ context.Context, _, project string) (string, error) {
	f.record("down " + project)
	if f.OnDown != nil {
		f.OnDown(project)
	}
	return "", nil
}

func (f *FakeCompose) Restart(_ context.Context, _, project, service string) (string, error) {
	f.record("restart " + project + " " + service)
	if f.OnRestart != nil {
		f.OnRestart(project, service)
	}
	return "", nil
}

func (f *FakeCompose) Stop(_ context.Context, _, project, service string) (string, error) {
	f.record("stop " + project + " " + service)
	if f.OnStop != nil {
		f.OnStop(project, service)
	}
	return "", nil
}

func (f *FakeCompose) TryLock(key string) (func(), bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.locks == nil {
		f.locks = map[string]bool{}
	}
	if f.locks[key] {
		return nil, false
	}
	f.locks[key] = true
	var once sync.Once
	return func() {
		once.Do(func() {
			f.mu.Lock()
			delete(f.locks, key)
			f.mu.Unlock()
		})
	}, true
}

type FakeDocker struct {
	mu         sync.Mutex
	Containers map[string]dockerx.ContainerInfo
	Facts      map[string]dockerx.Facts
	Networks   map[string]bool
	LogsOut    string
	touched    []string
}

func NewFakeDocker() *FakeDocker {
	return &FakeDocker{Containers: map[string]dockerx.ContainerInfo{}, Facts: map[string]dockerx.Facts{}, Networks: map[string]bool{}}
}

// Touched lists container IDs passed to Inspect, Logs or RemoveContainer.
func (d *FakeDocker) Touched() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.touched)
}

func (d *FakeDocker) Add(info dockerx.ContainerInfo, facts dockerx.Facts) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Containers[info.ID] = info
	d.Facts[info.ID] = facts
}

func (d *FakeDocker) MutateFacts(id string, f func(*dockerx.Facts)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	facts := d.Facts[id]
	f(&facts)
	d.Facts[id] = facts
}

func (d *FakeDocker) filter(match func(map[string]string) bool) []dockerx.ContainerInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := []dockerx.ContainerInfo{}
	for _, c := range d.Containers {
		if match(c.Labels) {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b dockerx.ContainerInfo) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func (d *FakeDocker) ProjectContainers(_ context.Context, ws, app string) ([]dockerx.ContainerInfo, error) {
	return d.filter(func(l map[string]string) bool { return l[names.LabelWorkspace] == ws && l[names.LabelApp] == app }), nil
}

func (d *FakeDocker) ManagedContainers(_ context.Context) ([]dockerx.ContainerInfo, error) {
	return d.filter(func(l map[string]string) bool { return l[names.LabelManaged] == "true" }), nil
}

func (d *FakeDocker) Inspect(_ context.Context, id string) (dockerx.Facts, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.touched = append(d.touched, id)
	f, ok := d.Facts[id]
	if !ok {
		return dockerx.Facts{}, errors.New("no such container: " + id)
	}
	return f, nil
}

func (d *FakeDocker) Logs(_ context.Context, id string, _ int, _ string, _ int) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.touched = append(d.touched, id)
	return d.LogsOut, nil
}

func (d *FakeDocker) NetworkExists(_ context.Context, name string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.Networks[name], nil
}

func (d *FakeDocker) RemoveContainer(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.touched = append(d.touched, id)
	delete(d.Containers, id)
	delete(d.Facts, id)
	return nil
}

type renderedFile struct {
	Services map[string]struct {
		Labels      map[string]string `yaml:"labels"`
		MemLimit    int64             `yaml:"mem_limit"`
		PidsLimit   int64             `yaml:"pids_limit"`
		Healthcheck map[string]any    `yaml:"healthcheck"`
		Networks    []string          `yaml:"networks"`
		Volumes     []struct {
			Type   string `yaml:"type"`
			Source string `yaml:"source"`
			Target string `yaml:"target"`
		} `yaml:"volumes"`
		Ports []struct {
			Target    int    `yaml:"target"`
			Published string `yaml:"published"`
			HostIP    string `yaml:"host_ip"`
			Protocol  string `yaml:"protocol"`
		} `yaml:"ports"`
	} `yaml:"services"`
}

// projectParts splits "dg-<ws>-<app>" (workspace names contain no hyphen).
func projectParts(project string) (ws, app string) {
	rest := strings.TrimPrefix(project, "dg-")
	i := strings.IndexByte(rest, '-')
	return rest[:i], rest[i+1:]
}

// Wire makes compose operations act on the fake Docker state.
func Wire(c *FakeCompose, d *FakeDocker) {
	c.OnUp = func(file, project string) error {
		raw, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		var doc renderedFile
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return err
		}
		ws, app := projectParts(project)
		d.mu.Lock()
		defer d.mu.Unlock()
		for id, ci := range d.Containers { // --remove-orphans
			if ci.Labels[names.LabelWorkspace] == ws && ci.Labels[names.LabelApp] == app {
				if _, keep := doc.Services[id]; !keep {
					delete(d.Containers, id)
					delete(d.Facts, id)
				}
			}
		}
		for key, s := range doc.Services {
			health := ""
			if s.Healthcheck != nil {
				health = "healthy"
			}
			f := dockerx.Facts{
				Name: key, Labels: s.Labels, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges:true"},
				NetworkMode: s.Networks[0], IpcMode: "private", Memory: s.MemLimit, PidsLimit: s.PidsLimit,
				State: "running", Health: health,
			}
			for _, v := range s.Volumes {
				f.Mounts = append(f.Mounts, dockerx.MountFact{Type: v.Type, Source: v.Source, Destination: v.Target})
			}
			for _, p := range s.Ports {
				hp, _ := strconv.Atoi(p.Published)
				f.PortBindings = append(f.PortBindings, dockerx.PortFact{ContainerPort: p.Target, Protocol: p.Protocol, HostIP: p.HostIP, HostPort: hp})
			}
			d.Containers[key] = dockerx.ContainerInfo{ID: key, Name: key, Labels: s.Labels, State: "running", Status: "Up", Health: health}
			d.Facts[key] = f
		}
		return nil
	}
	setState := func(project, service, state string) {
		ws, app := projectParts(project)
		d.mu.Lock()
		defer d.mu.Unlock()
		for id, ci := range d.Containers {
			if ci.Labels[names.LabelWorkspace] == ws && ci.Labels[names.LabelApp] == app && (service == "" || id == service) {
				ci.State = state
				d.Containers[id] = ci
			}
		}
	}
	c.OnStop = func(project, service string) { setState(project, service, "exited") }
	c.OnRestart = func(project, service string) { setState(project, service, "running") }
	c.OnDown = func(project string) {
		ws, app := projectParts(project)
		d.mu.Lock()
		defer d.mu.Unlock()
		for id, ci := range d.Containers {
			if ci.Labels[names.LabelWorkspace] == ws && ci.Labels[names.LabelApp] == app {
				delete(d.Containers, id)
				delete(d.Facts, id)
			}
		}
	}
}
