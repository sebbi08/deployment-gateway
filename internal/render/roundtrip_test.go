package render

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/sebbi08/deployment-gateway/internal/stack"
)

// Agent strings that yaml.v3 used to emit as broken or lossy block scalars.
var trickyArgs = []string{
	"\nset -e\nnode server.js\n",
	"  echo a\n# x",
	"\n  x",
	"\tindented\n\tbody",
	"\n0",
	"<<",
	"a: b # c",
	"line sep",
	"$HOME and $$",
	"trailing space ",
}

func renderOne(t *testing.T, svc stack.Service) (Output, map[string]any) {
	t.Helper()
	in := testInput(t)
	in.Stack = &stack.Stack{Services: map[string]stack.Service{"web": svc}}
	out, err := Render(in)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(out.YAML, &doc); err != nil {
		t.Fatalf("rendered YAML does not parse: %v\n%s", err, out.YAML)
	}
	return out, doc["services"].(map[string]any)["alice-shop-web"].(map[string]any)
}

func anyStrs(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func TestRenderKeepsAgentStringsExact(t *testing.T) {
	for _, s := range trickyArgs {
		t.Run(strings.ReplaceAll(s, "\n", `\n`), func(t *testing.T) {
			_, svc := renderOne(t, stack.Service{
				Image:       "a:1",
				Command:     []string{"sh", "-c", s},
				Entrypoint:  []string{s},
				Environment: map[string]string{"V": s},
				Healthcheck: &stack.Healthcheck{Command: []string{s}},
			})
			want := escape(s)
			if got := anyStrs(svc["command"]); !reflect.DeepEqual(got, []string{"sh", "-c", want}) {
				t.Errorf("command = %q, want %q", got, want)
			}
			if got := anyStrs(svc["entrypoint"]); !reflect.DeepEqual(got, []string{want}) {
				t.Errorf("entrypoint = %q", got)
			}
			if got := svc["environment"].(map[string]any)["V"]; got != want {
				t.Errorf("environment value = %q", got)
			}
			if got := anyStrs(svc["healthcheck"].(map[string]any)["test"]); !reflect.DeepEqual(got, []string{"CMD", want}) {
				t.Errorf("healthcheck test = %q", got)
			}
		})
	}
}

func TestRoundTripDetectsAlteredValues(t *testing.T) {
	file := composeFile{Services: map[string]composeService{
		"alice-shop-web": {Command: qstrs([]string{"sh", "-c", "  echo a\n# x"})},
	}}
	altered := []byte("services:\n  alice-shop-web:\n    command: [sh, -c, echo a]\n")
	err := roundTrip(altered, file)
	var fe *FieldError
	if !errors.As(err, &fe) || fe.Field != "services.web.command" {
		t.Fatalf("want FieldError on services.web.command, got %v", err)
	}
}
