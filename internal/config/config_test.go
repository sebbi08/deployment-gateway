package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadDefaultsPasswordMode(t *testing.T) {
	c, err := Load(env(map[string]string{
		"WORKSPACES_ROOT":     "/srv/dg/workspaces",
		"ADMIN_PASSWORD_HASH": "$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.AgentListen != ":8080" || c.AdminListen != ":8081" || c.DataDir != "/data" ||
		c.DockerConfig != "/docker" || c.ComposeBin != "/usr/local/bin/docker-compose" ||
		c.DockerHost != "unix:///var/run/docker.sock" ||
		c.DeployTimeout != 300*time.Second || c.DeployConcurrency != 1 || c.ProxyMode() {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestLoadProxyMode(t *testing.T) {
	c, err := Load(env(map[string]string{
		"WORKSPACES_ROOT":       "/srv/dg/workspaces",
		"ADMIN_TRUSTED_PROXIES": "172.20.0.10, 10.0.0.0/8",
		"ADMIN_USER_HEADER":     "Remote-User",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !c.ProxyMode() || len(c.AdminTrustedProxies) != 2 || c.AdminTrustedProxies[0].String() != "172.20.0.10/32" {
		t.Fatalf("unexpected: %+v", c)
	}
}

func TestLoadErrors(t *testing.T) {
	base := func() map[string]string {
		return map[string]string{"WORKSPACES_ROOT": "/srv/dg/workspaces", "ADMIN_PASSWORD_HASH": "$argon2id$x"}
	}
	cases := map[string]func(map[string]string){
		"missing root":     func(m map[string]string) { delete(m, "WORKSPACES_ROOT") },
		"relative root":    func(m map[string]string) { m["WORKSPACES_ROOT"] = "workspaces" },
		"unclean root":     func(m map[string]string) { m["WORKSPACES_ROOT"] = "/srv/dg/../x" },
		"proxy incomplete": func(m map[string]string) { delete(m, "ADMIN_PASSWORD_HASH"); m["ADMIN_USER_HEADER"] = "Remote-User" },
		"bad hash":         func(m map[string]string) { m["ADMIN_PASSWORD_HASH"] = "plaintext" },
		"bad proxies":      func(m map[string]string) { m["TRUSTED_PROXIES"] = "not-an-ip" },
		"trust all v4":     func(m map[string]string) { m["TRUSTED_PROXIES"] = "10.0.0.1, 0.0.0.0/0" },
		"trust all v6":     func(m map[string]string) { m["TRUSTED_PROXIES"] = "::/0" },
		"admin trust all":  func(m map[string]string) { m["ADMIN_TRUSTED_PROXIES"] = "0.0.0.0/0" },
		"admin trust all6": func(m map[string]string) { m["ADMIN_TRUSTED_PROXIES"] = "::/0" },
		"bad timeout":      func(m map[string]string) { m["DEPLOY_TIMEOUT"] = "1s" },
		"bad concurrency":  func(m map[string]string) { m["DEPLOY_CONCURRENCY"] = "0" },
	}
	for name, f := range cases {
		m := base()
		f(m)
		if _, err := Load(env(m)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestLoadReportsAllErrors(t *testing.T) {
	_, err := Load(env(map[string]string{"DEPLOY_CONCURRENCY": "x"}))
	if err == nil || !strings.Contains(err.Error(), "WORKSPACES_ROOT") || !strings.Contains(err.Error(), "DEPLOY_CONCURRENCY") {
		t.Fatalf("got %v", err)
	}
}

func TestLoadFileOwner(t *testing.T) {
	base := map[string]string{"WORKSPACES_ROOT": "/srv/dg/workspaces", "ADMIN_PASSWORD_HASH": "$argon2id$x"}
	c, err := Load(env(base))
	if err != nil || c.FileOwnerSet {
		t.Fatalf("unset FILE_OWNER: %+v %v", c, err)
	}
	base["FILE_OWNER"] = "1000:1001"
	c, err = Load(env(base))
	if err != nil || !c.FileOwnerSet || c.FileUID != 1000 || c.FileGID != 1001 {
		t.Fatalf("FILE_OWNER=1000:1001: %+v %v", c, err)
	}
	for _, bad := range []string{"abc", "1000", "1000:", "-1:2", "1000:1001:1", "4294967296:1", "sebastian:users"} {
		base["FILE_OWNER"] = bad
		if _, err := Load(env(base)); err == nil || !strings.Contains(err.Error(), "FILE_OWNER") {
			t.Errorf("FILE_OWNER=%q: want error, got %v", bad, err)
		}
	}
}
