package dockerx

import (
	"strings"
	"testing"
)

func goodFacts() Facts {
	return Facts{
		Name: "alice-shop-web",
		Labels: map[string]string{"deploy-gateway.workspace": "alice", "deploy-gateway.app": "shop",
			"traefik.enable": "true", "com.docker.compose.project": "dg-alice-shop", "org.opencontainers.image.title": "shop"},
		CapDrop:     []string{"ALL"},
		SecurityOpt: []string{"no-new-privileges:true"},
		NetworkMode: "dg-alice-shop",
		IpcMode:     "private",
		Memory:      256 << 20,
		PidsLimit:   256,
		Mounts: []MountFact{
			{Type: "bind", Source: "/srv/ws/alice/shop/data/web", Destination: "/data"},
			{Type: "volume", Name: strings.Repeat("a", 64), Destination: "/var/cache"},
		},
		PortBindings: []PortFact{{ContainerPort: 8080, Protocol: "tcp", HostIP: "0.0.0.0", HostPort: 20001}},
	}
}

func policy() RuntimePolicy {
	return RuntimePolicy{Workspace: "alice", App: "shop", DataDir: "/srv/ws/alice/shop/data",
		Ports:            []PortFact{{ContainerPort: 8080, Protocol: "tcp", HostIP: "0.0.0.0", HostPort: 20001}},
		Labels:           map[string]string{"deploy-gateway.workspace": "alice", "deploy-gateway.app": "shop", "traefik.enable": "true"},
		ReservedPrefixes: []string{"traefik.", "deploy-gateway.", "wud."}}
}

func TestCheckRuntimeAcceptsGood(t *testing.T) {
	if v := CheckRuntime(goodFacts(), policy()); len(v) != 0 {
		t.Fatalf("unexpected violations: %v", v)
	}
}

func TestCheckRuntimeRejects(t *testing.T) {
	cases := []struct {
		name string
		f    func(*Facts)
		want string
	}{
		{"privileged", func(f *Facts) { f.Privileged = true }, "privileged"},
		{"cap_add", func(f *Facts) { f.CapAdd = []string{"NET_ADMIN"} }, "cap_add"},
		{"no cap_drop", func(f *Facts) { f.CapDrop = nil }, "cap_drop"},
		{"cap_drop without ALL", func(f *Facts) { f.CapDrop = []string{"NET_RAW"} }, "cap_drop"},
		{"no nnp", func(f *Facts) { f.SecurityOpt = nil }, "no-new-privileges"},
		{"seccomp unconfined", func(f *Facts) { f.SecurityOpt = append(f.SecurityOpt, "seccomp=unconfined") }, "seccomp=unconfined"},
		{"apparmor unconfined", func(f *Facts) { f.SecurityOpt = append(f.SecurityOpt, "apparmor=unconfined") }, "apparmor=unconfined"},
		{"nnp false", func(f *Facts) { f.SecurityOpt = []string{"no-new-privileges:false"} }, "no-new-privileges:false"},
		{"nnp true then false", func(f *Facts) { f.SecurityOpt = []string{"no-new-privileges:true", "no-new-privileges=false"} }, "no-new-privileges=false"},
		{"host network", func(f *Facts) { f.NetworkMode = "host" }, "network mode"},
		{"container network", func(f *Facts) { f.NetworkMode = "container:abc" }, "network mode"},
		{"host pid", func(f *Facts) { f.PidMode = "host" }, "pid mode"},
		{"container pid", func(f *Facts) { f.PidMode = "container:abc" }, "pid mode"},
		{"host ipc", func(f *Facts) { f.IpcMode = "host" }, "ipc mode"},
		{"container ipc", func(f *Facts) { f.IpcMode = "container:abc" }, "ipc mode"},
		{"host uts", func(f *Facts) { f.UTSMode = "host" }, "uts mode"},
		{"host userns", func(f *Facts) { f.UsernsMode = "host" }, "userns mode"},
		{"runtime", func(f *Facts) { f.Runtime = "nvidia" }, "runtime"},
		{"devices", func(f *Facts) { f.Devices = 1 }, "devices"},
		{"no memory", func(f *Facts) { f.Memory = 0 }, "memory"},
		{"no pids", func(f *Facts) { f.PidsLimit = 0 }, "pids"},
		// The API reports -1 for an unlimited pids limit.
		{"unlimited pids", func(f *Facts) { f.PidsLimit = -1 }, "pids"},
		{"bind outside", func(f *Facts) { f.Mounts[0].Source = "/etc" }, "bind mount"},
		{"bind prefix trick", func(f *Facts) { f.Mounts[0].Source = "/srv/ws/alice/shop/data-evil" }, "bind mount"},
		{"bind of the whole data dir", func(f *Facts) { f.Mounts[0].Source = "/srv/ws/alice/shop/data/" }, "bind mount"},
		{"docker.sock", func(f *Facts) { f.Mounts[0].Source = "/var/run/docker.sock" }, "bind mount"},
		{"named volume", func(f *Facts) { f.Mounts[1].Name = "shared-data" }, "named volume"},
		{"named volume with hex prefix", func(f *Facts) { f.Mounts[1].Name = strings.Repeat("a", 64) + "-shared" }, "named volume"},
		{"named volume with hex suffix", func(f *Facts) { f.Mounts[1].Name = "shared-" + strings.Repeat("a", 64) }, "named volume"},
		{"other mount type", func(f *Facts) { f.Mounts[1].Type = "npipe" }, "mount type"},
		{"owner mismatch", func(f *Facts) { f.Labels["deploy-gateway.workspace"] = "bob" }, "owner labels"},
		{"app mismatch", func(f *Facts) { f.Labels["deploy-gateway.app"] = "blog" }, "owner labels"},
		{"extra port", func(f *Facts) {
			f.PortBindings = append(f.PortBindings, PortFact{ContainerPort: 22, Protocol: "tcp", HostIP: "0.0.0.0", HostPort: 22})
		}, "port bindings"},
		{"wrong host ip", func(f *Facts) { f.PortBindings[0].HostIP = "" }, "port bindings"},
		{"wrong protocol", func(f *Facts) { f.PortBindings[0].Protocol = "udp" }, "port bindings"},
		{"wrong host port", func(f *Facts) { f.PortBindings[0].HostPort = 20002 }, "port bindings"},
		{"unexpected traefik label", func(f *Facts) { f.Labels["traefik.http.routers.pwn.rule"] = "Host(`admin.example.com`)" }, "traefik.http.routers.pwn.rule"},
		{"mixed-case traefik label", func(f *Facts) { f.Labels["Traefik.HTTP.Routers.pwn.rule"] = "Host(`x`)" }, "Traefik.HTTP.Routers.pwn.rule"},
		{"changed traefik label", func(f *Facts) { f.Labels["traefik.enable"] = "false" }, "traefik.enable"},
		{"template namespace label", func(f *Facts) { f.Labels["wud.watch"] = "true" }, "wud.watch"},
		{"extra owner label", func(f *Facts) { f.Labels["deploy-gateway.extra"] = "x" }, "deploy-gateway.extra"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := goodFacts()
			c.f(&f)
			v := CheckRuntime(f, policy())
			if len(v) == 0 || !strings.Contains(strings.Join(v, "; "), c.want) {
				t.Fatalf("want violation containing %q, got %v", c.want, v)
			}
		})
	}
}

func TestHasReservedPrefix(t *testing.T) {
	prefixes := []string{"traefik.", "wud.", "foo"}
	for key, want := range map[string]bool{
		"traefik.http.routers.x.rule": true,
		"Traefik.HTTP.routers.x.rule": true,
		"traefik":                     true,
		"traefikx.enable":             false,
		"wud.watch":                   true,
		"foobar":                      true,
		"org.opencontainers.image":    false,
		"maintainer":                  false,
	} {
		if got := HasReservedPrefix(key, prefixes); got != want {
			t.Errorf("HasReservedPrefix(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestCheckRuntimeAcceptsNoNewPrivilegesSpellings(t *testing.T) {
	for _, opts := range [][]string{{"no-new-privileges"}, {"no-new-privileges:true"}, {"no-new-privileges=true"}, {"no-new-privileges", "no-new-privileges:true"}} {
		f := goodFacts()
		f.SecurityOpt = opts
		if v := CheckRuntime(f, policy()); len(v) != 0 {
			t.Errorf("%v: unexpected violations %v", opts, v)
		}
	}
}

// Compose sets com.docker.compose.* itself (images carrying them are rejected
// before up), so they never count as unrendered even if a template reserves
// the broader "com." namespace.
func TestCheckRuntimeIgnoresComposeLabels(t *testing.T) {
	p := policy()
	p.ReservedPrefixes = append(p.ReservedPrefixes, "com.")
	f := goodFacts()
	if v := CheckRuntime(f, p); len(v) != 0 {
		t.Fatalf("compose labels flagged: %v", v)
	}
	f.Labels["com.example.route"] = "x"
	if v := CheckRuntime(f, p); len(v) != 1 || !strings.Contains(v[0], "com.example.route") {
		t.Fatalf("unrendered com.example label not flagged: %v", v)
	}
}

func TestCheckRuntimePreExistingSkipsOnlyRenderMatch(t *testing.T) {
	f := goodFacts()
	f.Labels["traefik.http.routers.old.rule"] = "Host(`old.example.com`)"
	f.PortBindings = []PortFact{{ContainerPort: 80, Protocol: "tcp", HostIP: "0.0.0.0", HostPort: 20001}}
	p := policy()
	p.PreExisting = true
	if v := CheckRuntime(f, p); len(v) != 0 {
		t.Fatalf("pre-existing container with an older render flagged: %v", v)
	}
	f.Privileged = true
	if v := CheckRuntime(f, p); len(v) != 1 || !strings.Contains(v[0], "privileged") {
		t.Fatalf("plan-independent checks must still apply: %v", v)
	}
	p.PreExisting = false
	f.Privileged = false
	if v := CheckRuntime(f, p); len(v) != 2 {
		t.Fatalf("new container must be matched against the render: %v", v)
	}
}

func TestCheckRuntimeAllowedRuntimes(t *testing.T) {
	for _, tc := range []struct {
		runtime string
		extra   []string
		ok      bool
	}{
		{"", nil, true},
		{"runc", nil, true},
		{"io.containerd.runc.v2", nil, true},
		{"runsc", nil, false},
		{"runsc", []string{"runsc"}, true},
		{"nvidia", nil, false},
	} {
		f := goodFacts()
		f.Runtime = tc.runtime
		p := policy()
		p.ExtraRuntimes = tc.extra
		v := CheckRuntime(f, p)
		if ok := len(v) == 0; ok != tc.ok {
			t.Errorf("runtime %q extra %v: violations %v, want ok=%v", tc.runtime, tc.extra, v, tc.ok)
		}
	}
	if !RuntimeAllowed("io.containerd.runc.v2", nil) || RuntimeAllowed("nvidia", []string{"nvidia"}) {
		t.Fatal("RuntimeAllowed")
	}
}
