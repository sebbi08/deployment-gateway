package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sebmahr/deploy-gateway/internal/agentauth"
	"github.com/sebmahr/deploy-gateway/internal/auth"
	"github.com/sebmahr/deploy-gateway/internal/service"
	"github.com/sebmahr/deploy-gateway/internal/service/servicetest"
	"github.com/sebmahr/deploy-gateway/internal/store"
)

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func setup(t *testing.T) (string, string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	compose, docker := &servicetest.FakeCompose{}, servicetest.NewFakeDocker()
	servicetest.Wire(compose, docker)
	svc := service.New(st, compose, docker, t.TempDir())
	svc.Chown = func(string, int, int) error { return nil }
	w, err := svc.CreateWorkspace(ctx, "admin", "alice", store.BuiltinDefaultQuota(), service.PortSettings{BindIP: "0.0.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := svc.CreateToken(ctx, "admin", w, "ci")
	if err != nil {
		t.Fatal(err)
	}
	authn := &agentauth.Authenticator{Store: st, Limiter: auth.NewLimiter(10, time.Minute, time.Hour)}
	srv := httptest.NewServer(Handler(New(svc, "test"), authn))
	t.Cleanup(srv.Close)
	return srv.URL, tok
}

func connect(t *testing.T, url, token string) (*mcp.ClientSession, error) {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	return client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: url, HTTPClient: &http.Client{Transport: bearer{token}},
	}, nil)
}

func TestToolsOverStreamableHTTP(t *testing.T) {
	url, tok := setup(t)
	cs, err := connect(t, url, tok)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	ctx := context.Background()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
	}
	slices.Sort(names)
	want := []string{"delete", "deploy", "list", "list_templates", "logs", "restart", "stop", "validate", "whoami"}
	if !slices.Equal(names, want) {
		t.Fatalf("tools = %v", names)
	}

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "whoami", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("whoami: %v %+v", err, res)
	}
	if res.StructuredContent.(map[string]any)["workspace"] != "alice" {
		t.Fatalf("structured = %v", res.StructuredContent)
	}

	stack := map[string]any{"services": map[string]any{"web": map[string]any{"image": "nginx:1.27.0"}}}
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "deploy", Arguments: map[string]any{"app": "shop", "stack": stack}})
	if err != nil || res.IsError {
		t.Fatalf("deploy: %v %+v", err, res)
	}

	bad := map[string]any{"services": map[string]any{"web": map[string]any{"image": "nginx"}}}
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "deploy", Arguments: map[string]any{"app": "shop", "stack": bad}})
	if err != nil || !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "validation_failed") {
		t.Fatalf("expected tool error with validation_failed: %v %+v", err, res)
	}

	sneaky := map[string]any{"services": map[string]any{"web": map[string]any{"image": "nginx:1.27.0", "privileged": true}}}
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "deploy", Arguments: map[string]any{"app": "shop", "stack": sneaky}})
	if err == nil && !res.IsError {
		t.Fatal("unknown field privileged must be rejected")
	}
}

func TestRejectsBadToken(t *testing.T) {
	url, _ := setup(t)
	if _, err := connect(t, url, "dgw_"+strings.Repeat("x", 43)); err == nil {
		t.Fatal("connect with a bad token must fail")
	}
}
