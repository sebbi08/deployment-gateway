package tmpl

import (
	"strings"
	"testing"
)

func publicWeb() Template {
	return Template{
		Name:        "public-web",
		Description: "Public HTTPS via Traefik",
		HostPattern: "{{app}}.{{workspace}}.apps.example.com",
		Networks:    []string{"apps"},
		Labels: map[string]string{
			"traefik.enable":                                        "true",
			"traefik.docker.network":                                "apps",
			"traefik.http.routers.{{id}}.rule":                      "Host(`{{host}}`)",
			"traefik.http.routers.{{id}}.entrypoints":               "websecure",
			"traefik.http.services.{{id}}.loadbalancer.server.port": "{{port}}",
		},
	}
}

func TestValidateAcceptsExamples(t *testing.T) {
	ok := []Template{
		publicWeb(),
		{Name: "per-service", HostPattern: "{{service}}-{{app}}-{{workspace}}.svc.example.com", Networks: []string{"apps"}},
		{Name: "internal", Networks: []string{"apps"}, Labels: map[string]string{"traefik.enable": "false"}},
		{Name: "shared-middleware", Networks: []string{"apps"}, Labels: map[string]string{
			"traefik.http.middlewares.compress.compress":              "true",
			"traefik.http.routers.{{id}}.middlewares":                 "compress@docker",
			"traefik.http.services.{{id}}-x.loadbalancer.server.port": "{{port}}",
		}},
	}
	for _, tpl := range ok {
		if err := Validate(tpl, nil); err != nil {
			t.Errorf("%s: unexpected error %v", tpl.Name, err)
		}
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Template)
		want   string
	}{
		{"bad name", func(t *Template) { t.Name = "Public" }, "name"},
		{"description too long", func(t *Template) { t.Description = strings.Repeat("d", 501) }, "description longer than 500"},
		{"literal in subdomain", func(t *Template) { t.HostPattern = "app-{{app}}.{{workspace}}.example.com" }, "host_pattern must be"},
		{"adjacent placeholders", func(t *Template) { t.HostPattern = "{{app}}{{workspace}}.example.com" }, "host_pattern must be"},
		{"underscore separator", func(t *Template) { t.HostPattern = "{{app}}_{{workspace}}.example.com" }, "host_pattern must be"},
		{"missing workspace", func(t *Template) { t.HostPattern = "{{app}}.apps.example.com" }, "{{workspace}}"},
		{"missing app", func(t *Template) { t.HostPattern = "{{workspace}}.apps.example.com" }, "must contain {{workspace}} and {{app}}"},
		{"duplicate placeholder", func(t *Template) { t.HostPattern = "{{app}}.{{app}}.{{workspace}}.example.com" }, "more than once"},
		{"single label suffix", func(t *Template) { t.HostPattern = "{{app}}.{{workspace}}.localhost" }, "at least two labels"},
		{"uppercase suffix", func(t *Template) { t.HostPattern = "{{app}}.{{workspace}}.Example.com" }, "host_pattern must be"},
		{"invalid suffix label", func(t *Template) { t.HostPattern = "{{app}}.{{workspace}}.-apps.example.com" }, "host suffix"},
		{"longest host over 253", func(t *Template) {
			l := strings.Repeat("a", 60)
			t.HostPattern = "{{app}}.{{workspace}}." + strings.Join([]string{l, l, l, l}, ".")
		}, "longest possible host"},
		{"host network", func(t *Template) { t.Networks = []string{"host"} }, `"host"`},
		{"bridge network", func(t *Template) { t.Networks = []string{"bridge"} }, `"bridge"`},
		{"none network", func(t *Template) { t.Networks = []string{"none"} }, `"none"`},
		{"invalid network name", func(t *Template) { t.Networks = []string{"apps net"} }, "invalid network name"},
		{"reserved dg- network", func(t *Template) { t.Networks = []string{"dg-alice-shop"} }, "dg-"},
		{"owner label", func(t *Template) { t.Labels["deploy-gateway.workspace"] = "x" }, "deploy-gateway."},
		{"owner label upper case", func(t *Template) { t.Labels["Deploy-Gateway.workspace"] = "x" }, "the deploy-gateway. prefix is reserved"},
		{"invalid label key", func(t *Template) { t.Labels["traefik enable"] = "x" }, "invalid label key"},
		{"compose label", func(t *Template) { t.Labels["Com.Docker.Compose.project"] = "x" }, "com.docker.compose."},
		{"placeholder completes owner prefix", func(t *Template) { t.Labels["deploy-{{app}}.workspace"] = "bob" }, "deploy-gateway."},
		{"placeholder completes compose prefix", func(t *Template) { t.Labels["com.docker.{{app}}.project"] = "x" }, "com.docker.compose."},
		{"label key starts with placeholder", func(t *Template) { t.Labels["{{app}}.x"] = "y" }, "must not start with a placeholder"},
		{"unknown placeholder", func(t *Template) { t.Labels["x"] = "{{secret}}" }, "unknown placeholder"},
		{"malformed placeholder", func(t *Template) { t.Labels["x"] = "{{app" }, "malformed"},
		{"stray closing braces", func(t *Template) { t.Labels["x"] = "app}}" }, "malformed"},
		{"newline in value", func(t *Template) { t.Labels["x"] = "a\nb" }, "newline"},
		{"carriage return in value", func(t *Template) { t.Labels["x"] = "a\rb" }, "newline"},
		{"NUL in value", func(t *Template) { t.Labels["x"] = "a\x00b" }, "NUL"},
		{"value too long", func(t *Template) { t.Labels["x"] = strings.Repeat("v", 4097) }, "longer than 4096"},
		{"host without pattern", func(t *Template) { t.HostPattern = "" }, "{{host}}"},
		{"keys collide after rendering", func(t *Template) {
			t.Labels["traefik.http.routers.{{workspace}}-{{app}}-{{service}}.rule"] = "Host(`x`)"
		}, "render to the same label"},
		{"keys collide when app equals service", func(t *Template) {
			t.Labels["x.{{app}}"] = "1"
			t.Labels["x.{{service}}"] = "2"
		}, "render to the same label"},
		{"keys differ only in case", func(t *Template) { t.Labels["Traefik.enable"] = "false" }, "render to the same label"},
		{"router name without {{id}}", func(t *Template) { t.Labels["traefik.http.routers.{{app}}.rule"] = "Host(`x`)" }, "must contain {{id}}"},
		{"literal service name", func(t *Template) { t.Labels["traefik.http.services.web.loadbalancer.server.port"] = "80" }, "must contain {{id}}"},
		{"tcp router name without {{id}}", func(t *Template) { t.Labels["traefik.tcp.routers.db-{{workspace}}.rule"] = "HostSNI(`*`)" }, "must contain {{id}}"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tpl := publicWeb()
			c.mutate(&tpl)
			err := Validate(tpl, nil)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error containing %q, got %v", c.want, err)
			}
		})
	}
}

func TestValidateSuffixOverlap(t *testing.T) {
	other := publicWeb() // suffix apps.example.com
	cases := map[string]bool{
		"{{app}}-{{workspace}}.apps.example.com":   true,  // equal
		"{{app}}-{{workspace}}.x.apps.example.com": true,  // nested below
		"{{app}}-{{workspace}}.example.com":        true,  // nested above
		"{{app}}-{{workspace}}.myapps.example.com": false, // not on a label boundary
		"{{app}}-{{workspace}}.apps.example.org":   false,
	}
	for pattern, wantErr := range cases {
		tpl := Template{Name: "second", HostPattern: pattern, Networks: []string{"apps"}}
		err := Validate(tpl, []Template{other})
		if (err != nil) != wantErr {
			t.Errorf("%s: wantErr=%v got %v", pattern, wantErr, err)
		}
	}
	// Editing the same template must not conflict with its own stored version.
	if err := Validate(publicWeb(), []Template{publicWeb()}); err != nil {
		t.Fatalf("self overlap reported: %v", err)
	}
}

func TestRender(t *testing.T) {
	tpl := publicWeb()
	v := Vars{Workspace: "alice", App: "my-shop", Service: "web", Port: 8080}
	if got := tpl.Host(v); got != "my-shop.alice.apps.example.com" {
		t.Fatalf("Host = %q", got)
	}
	labels := tpl.RenderLabels(v)
	if labels["traefik.http.routers.alice-my-shop-web.rule"] != "Host(`my-shop.alice.apps.example.com`)" {
		t.Fatalf("labels = %v", labels)
	}
	if labels["traefik.http.services.alice-my-shop-web.loadbalancer.server.port"] != "8080" {
		t.Fatalf("labels = %v", labels)
	}
	// Each name placeholder renders its own value, in keys and values.
	tpl.Labels["x.{{workspace}}.{{app}}.{{service}}"] = "{{service}}/{{app}}/{{workspace}}"
	if got := tpl.RenderLabels(v)["x.alice.my-shop.web"]; got != "web/my-shop/alice" {
		t.Fatalf("name placeholders rendered %q, labels = %v", got, tpl.RenderLabels(v))
	}
	if tpl.Shareable() {
		t.Fatal("public-web has no {{service}}; must not be shareable")
	}
	if !(Template{HostPattern: "{{service}}.{{app}}.{{workspace}}.a.b"}).Shareable() || !(Template{}).Shareable() {
		t.Fatal("expected shareable")
	}
	if HostSuffix(tpl.HostPattern) != "apps.example.com" {
		t.Fatalf("HostSuffix = %q", HostSuffix(tpl.HostPattern))
	}
}

func TestHostsAreUniqueWithinTemplate(t *testing.T) {
	patterns := []string{
		"{{service}}-{{app}}-{{workspace}}.a.example.com",
		"{{workspace}}-{{app}}-{{service}}.a.example.com",
		"{{app}}-{{service}}-{{workspace}}.a.example.com",
		"{{service}}.{{app}}.{{workspace}}.a.example.com",
	}
	wss := []string{"ab", "abc", "a1"}
	apps := []string{"x1", "x1-ab", "ab-x1", "x1-x1", "ab"}
	svcs := []string{"ab", "x1", "abc"}
	for _, p := range patterns {
		tpl := Template{HostPattern: p}
		seen := map[string]string{}
		for _, w := range wss {
			for _, a := range apps {
				for _, s := range svcs {
					h := tpl.Host(Vars{Workspace: w, App: a, Service: s})
					key := w + "|" + a + "|" + s
					if prev, dup := seen[h]; dup {
						t.Fatalf("%s: %s and %s both produce %s", p, prev, key, h)
					}
					seen[h] = key
				}
			}
		}
	}
}

func TestLabelPrefixes(t *testing.T) {
	ts := []Template{
		publicWeb(),
		{Name: "wud", Labels: map[string]string{"wud.watch": "true", "WUD.tag.include": `^\d+$`}},
		{Name: "flat", Labels: map[string]string{"homepage": "x", "foo{{app}}.bar": "y"}},
		{Name: "none"},
	}
	got := LabelPrefixes(ts)
	want := []string{"foo", "homepage.", "traefik.", "wud."}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("LabelPrefixes = %v, want %v", got, want)
	}
	if got := LabelPrefixes(nil); len(got) != 0 {
		t.Fatalf("no templates: %v", got)
	}
}
