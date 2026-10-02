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
		{"no nnp", func(f *Facts) { f.SecurityOpt = nil }, "no-new-privileges"},
		{"host network", func(f *Facts) { f.NetworkMode = "host" }, "network mode"},
		{"container network", func(f *Facts) { f.NetworkMode = "container:abc" }, "network mode"},
		{"host pid", func(f *Facts) { f.PidMode = "host" }, "pid mode"},
		{"host userns", func(f *Facts) { f.UsernsMode = "host" }, "userns mode"},
		{"runtime", func(f *Facts) { f.Runtime = "nvidia" }, "runtime"},
		{"devices", func(f *Facts) { f.Devices = 1 }, "devices"},
		{"no memory", func(f *Facts) { f.Memory = 0 }, "memory"},
		{"no pids", func(f *Facts) { f.PidsLimit = 0 }, "pids"},
		{"bind outside", func(f *Facts) { f.Mounts[0].Source = "/etc" }, "bind mount"},
		{"bind prefix trick", func(f *Facts) { f.Mounts[0].Source = "/srv/ws/alice/shop/data-evil" }, "bind mount"},
		{"docker.sock", func(f *Facts) { f.Mounts[0].Source = "/var/run/docker.sock" }, "bind mount"},
		{"named volume", func(f *Facts) { f.Mounts[1].Name = "shared-data" }, "named volume"},
		{"other mount type", func(f *Facts) { f.Mounts[1].Type = "npipe" }, "mount type"},
		{"owner mismatch", func(f *Facts) { f.Labels["deploy-gateway.workspace"] = "bob" }, "owner labels"},
		{"extra port", func(f *Facts) {
			f.PortBindings = append(f.PortBindings, PortFact{ContainerPort: 22, Protocol: "tcp", HostIP: "0.0.0.0", HostPort: 22})
		}, "port bindings"},
		{"wrong host ip", func(f *Facts) { f.PortBindings[0].HostIP = "" }, "port bindings"},
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
