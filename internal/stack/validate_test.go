package stack

import (
	"strings"
	"testing"
)

func testLimits() Limits {
	return Limits{MaxServices: 4, MaxMemoryBytes: 1 << 30, MaxCPUs: 1, MaxPids: 1024, PortRangeStart: 20000, PortRangeEnd: 20099}
}

func testGrants() Grants { return Grants{"public-web": false, "internal": true} }

func validStack(t *testing.T) *Stack {
	t.Helper()
	s, err := Decode([]byte(exampleJSON))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func edit(s *Stack, name string, f func(*Service)) {
	v := s.Services[name]
	f(&v)
	s.Services[name] = v
}

func TestValidateAcceptsExample(t *testing.T) {
	if errs := Validate(validStack(t), testLimits(), testGrants()); errs != nil {
		t.Fatalf("unexpected errors: %v", errs)
	}
}

func TestValidateAcceptsDigestAndShareableTemplate(t *testing.T) {
	s := validStack(t)
	edit(s, "db", func(v *Service) {
		v.Image = "postgres@sha256:" + strings.Repeat("a", 64)
		v.Expose = &Expose{Template: "internal", Port: 5432}
	})
	edit(s, "web", func(v *Service) { v.Expose = &Expose{Template: "internal", Port: 8080} })
	if errs := Validate(s, testLimits(), testGrants()); errs != nil {
		t.Fatalf("unexpected errors: %v", errs)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(s *Stack, l *Limits)
		want   string
	}{
		{"no services", func(s *Stack, _ *Limits) { s.Services = nil }, "at least one service"},
		{"too many services", func(s *Stack, _ *Limits) {
			for _, n := range []string{"a", "b", "c"} {
				s.Services[n] = Service{Image: "a:1"}
			}
		}, "at most 4"},
		{"bad service name", func(s *Stack, _ *Limits) { s.Services["my-db"] = Service{Image: "a:1"} }, "services.my-db"},
		{"missing image", func(s *Stack, _ *Limits) { edit(s, "web", func(v *Service) { v.Image = "" }) }, "services.web.image"},
		{"latest tag", func(s *Stack, _ *Limits) { edit(s, "web", func(v *Service) { v.Image = "nginx:latest" }) }, `"latest"`},
		{"no tag", func(s *Stack, _ *Limits) { edit(s, "web", func(v *Service) { v.Image = "nginx" }) }, "explicit tag"},
		{"bad image", func(s *Stack, _ *Limits) { edit(s, "web", func(v *Service) { v.Image = "ghcr.io/UPPER:1" }) }, "invalid image"},
		{"env bad key", func(s *Stack, _ *Limits) {
			edit(s, "web", func(v *Service) { v.Environment["BAD-KEY"] = "x" })
		}, "services.web.environment.BAD-KEY"},
		{"env NUL", func(s *Stack, _ *Limits) {
			edit(s, "web", func(v *Service) { v.Environment["A"] = "a\x00b" })
		}, "NUL"},
		{"env value too large", func(s *Stack, _ *Limits) {
			edit(s, "web", func(v *Service) { v.Environment["A"] = strings.Repeat("x", 9000) })
		}, "8192"},
		{"command too many items", func(s *Stack, _ *Limits) {
			edit(s, "web", func(v *Service) { v.Command = make([]string, 33) })
		}, "services.web.command"},
		{"expose not granted", func(s *Stack, _ *Limits) {
			edit(s, "web", func(v *Service) { v.Expose.Template = "nope" })
		}, "not granted"},
		{"expose port", func(s *Stack, _ *Limits) {
			edit(s, "web", func(v *Service) { v.Expose.Port = 0 })
		}, "services.web.expose.port"},
		{"non-shareable template twice", func(s *Stack, _ *Limits) {
			edit(s, "db", func(v *Service) { v.Expose = &Expose{Template: "public-web", Port: 5432} })
		}, "only one service"},
		{"port outside range", func(s *Stack, _ *Limits) {
			edit(s, "web", func(v *Service) { v.Ports[0].Host = 30000 })
		}, "20000-20099"},
		{"ports without range", func(s *Stack, l *Limits) { l.PortRangeStart, l.PortRangeEnd = 0, 0 }, "may not publish"},
		{"duplicate host port", func(s *Stack, _ *Limits) {
			edit(s, "db", func(v *Service) { v.Ports = []Port{{Container: 5432, Host: 20001}} })
		}, "already used"},
		{"bad protocol", func(s *Stack, _ *Limits) {
			edit(s, "web", func(v *Service) { v.Ports[0].Protocol = "sctp" })
		}, "tcp or udp"},
		{"volume relative", func(s *Stack, _ *Limits) { edit(s, "db", func(v *Service) { v.Volume.Path = "data" }) }, "services.db.volume.path"},
		{"volume traversal", func(s *Stack, _ *Limits) { edit(s, "db", func(v *Service) { v.Volume.Path = "/data/../etc" }) }, "normalized"},
		{"volume proc", func(s *Stack, _ *Limits) { edit(s, "db", func(v *Service) { v.Volume.Path = "/proc/x" }) }, "/proc"},
		{"volume docker.sock", func(s *Stack, _ *Limits) { edit(s, "db", func(v *Service) { v.Volume.Path = "/var/run/docker.sock" }) }, "docker.sock"},
		{"volume tmp", func(s *Stack, _ *Limits) { edit(s, "db", func(v *Service) { v.Volume.Path = "/tmp" }) }, "/tmp"},
		{"volume root", func(s *Stack, _ *Limits) { edit(s, "db", func(v *Service) { v.Volume.Path = "/" }) }, "must not be /"},
		{"volume dollar", func(s *Stack, _ *Limits) { edit(s, "db", func(v *Service) { v.Volume.Path = "/data$HOME" }) }, "services.db.volume.path"},
		{"healthcheck interval", func(s *Stack, _ *Limits) {
			edit(s, "web", func(v *Service) { v.Healthcheck.Interval = "1s" })
		}, "between 5s and 5m"},
		{"healthcheck no command", func(s *Stack, _ *Limits) {
			edit(s, "web", func(v *Service) { v.Healthcheck.Command = nil })
		}, "services.web.healthcheck.command"},
		{"depends unknown", func(s *Stack, _ *Limits) {
			edit(s, "web", func(v *Service) { v.DependsOn = []string{"cache"} })
		}, "unknown service"},
		{"depends self", func(s *Stack, _ *Limits) {
			edit(s, "web", func(v *Service) { v.DependsOn = []string{"web"} })
		}, "itself"},
		{"depends cycle", func(s *Stack, _ *Limits) {
			edit(s, "db", func(v *Service) { v.DependsOn = []string{"web"} })
		}, "cycle"},
		{"memory over max", func(s *Stack, _ *Limits) {
			edit(s, "web", func(v *Service) { v.Resources.Memory = "2g" })
		}, "services.web.resources.memory"},
		{"memory format", func(s *Stack, _ *Limits) {
			edit(s, "web", func(v *Service) { v.Resources.Memory = "256mb" })
		}, "like 256m"},
		{"cpus over max", func(s *Stack, _ *Limits) {
			edit(s, "web", func(v *Service) { v.Resources.CPUs = 2 })
		}, "services.web.resources.cpus"},
		{"pids too low", func(s *Stack, _ *Limits) {
			edit(s, "web", func(v *Service) { v.Resources.Pids = 5 })
		}, "services.web.resources.pids"},
		{"user non numeric", func(s *Stack, _ *Limits) { edit(s, "db", func(v *Service) { v.User = "postgres" }) }, "uid:gid"},
		{"uid too large", func(s *Stack, _ *Limits) { edit(s, "db", func(v *Service) { v.User = "4294967296:0" }) }, "services.db.user"},
		{"gid too large", func(s *Stack, _ *Limits) { edit(s, "db", func(v *Service) { v.User = "0:9999999999" }) }, "services.db.user"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, lim := validStack(t), testLimits()
			c.mutate(s, &lim)
			errs := Validate(s, lim, testGrants())
			if errs == nil || !strings.Contains(errs.Error(), c.want) {
				t.Fatalf("want error containing %q, got %v", c.want, errs)
			}
		})
	}
}

func TestParseMemory(t *testing.T) {
	cases := map[string]int64{"16m": 16 << 20, "1g": 1 << 30, "512k": 512 << 10, "256M": 256 << 20}
	for in, want := range cases {
		got, err := ParseMemory(in)
		if err != nil || got != want {
			t.Errorf("ParseMemory(%q) = %d, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "0m", "256mb", "-1m", "1t", "1.5g"} {
		if _, err := ParseMemory(bad); err == nil {
			t.Errorf("ParseMemory(%q) should fail", bad)
		}
	}
}

func TestEffectiveAndTotals(t *testing.T) {
	s := validStack(t)
	d := Defaults{MemoryBytes: 128 << 20, CPUs: 0.25, Pids: 256}
	web := Effective(s.Services["web"], d)
	if web.MemoryBytes != 256<<20 || web.CPUs != 0.5 || web.Pids != 256 {
		t.Fatalf("web = %+v", web)
	}
	mem, cpus := Totals(s, d)
	if mem != (256+128)<<20 || cpus != 0.75 {
		t.Fatalf("totals = %d %v", mem, cpus)
	}
}

func TestValidateAcceptsMaxUIDGID(t *testing.T) {
	s := validStack(t)
	edit(s, "db", func(v *Service) { v.User = "4294967295:4294967295" })
	if errs := Validate(s, testLimits(), testGrants()); errs != nil {
		t.Fatalf("unexpected errors: %v", errs)
	}
}
