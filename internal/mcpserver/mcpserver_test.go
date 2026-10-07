package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sebbi08/deployment-gateway/internal/agentauth"
	"github.com/sebbi08/deployment-gateway/internal/auth"
	"github.com/sebbi08/deployment-gateway/internal/service"
	"github.com/sebbi08/deployment-gateway/internal/service/servicetest"
	"github.com/sebbi08/deployment-gateway/internal/store"
)

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func setup(t *testing.T) (string, string) {
	t.Helper()
	url, tok, _, _ := setupWithStore(t)
	return url, tok
}

func setupWithStore(t *testing.T) (string, string, *store.Store, *servicetest.FakeDocker) {
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
	return srv.URL, tok, st, docker
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

func TestToolErrorMasksNonServiceErrors(t *testing.T) {
	se := &service.Error{Code: service.CodeNotFound, Message: "no such app"}
	if got := toolError(se); got.Error() != se.Error() || !strings.Contains(got.Error(), "no such app") {
		t.Fatalf("service error altered: %q", got)
	}
	for _, in := range []error{errors.New("open /srv/secret: permission denied"), fmt.Errorf("wrap: %w", errors.New("/srv/secret"))} {
		got := toolError(in).Error()
		if got != `{"code":"internal","message":"internal error"}` || strings.Contains(got, "/srv/secret") {
			t.Fatalf("not masked: %q", got)
		}
	}
}

func rawPost(t *testing.T, url, token, body string, version string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", url, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if version != "" {
		req.Header.Set("Mcp-Protocol-Version", version)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// The SDK accepts batches for protocol versions before 2025-06-18 and runs
// every call concurrently, so one request could hold gigabytes of results.
func TestBatchesRejectedForEveryVersion(t *testing.T) {
	url, tok := setup(t)
	batch := `[{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"whoami","arguments":{}}},
	           {"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"whoami","arguments":{}}}]`
	for _, v := range []string{"", "2024-11-05", "2025-03-26", "2025-06-18"} {
		if resp := rawPost(t, url, tok, "  \n"+batch, v); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("version %q: batch got %d, want 400", v, resp.StatusCode)
		}
	}
}

func TestOversizeBodyRejected(t *testing.T) {
	url, tok := setup(t)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"validate","arguments":{"app":"shop","stack":{"services":{"web":{"image":"a:1","environment":{"X":"` +
		strings.Repeat("x", 200<<10) + `"}}}}}}}`
	if resp := rawPost(t, url, tok, body, "2025-06-18"); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("200 KB body got %d, want 413", resp.StatusCode)
	}
}

// The SDK rejects arguments that fail the tool's input schema before our
// handler runs; such calls must still leave an audit row.
func TestSchemaRejectedToolCallsAreAudited(t *testing.T) {
	url, tok, st, _ := setupWithStore(t)
	cs, err := connect(t, url, tok)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "validate",
		Arguments: map[string]any{"app": "shop", "stack": map[string]any{"services": map[string]any{"web": map[string]any{"image": "a:1", "cap_add": []string{"ALL"}}}}}})
	if err != nil || !res.IsError {
		t.Fatalf("want a tool error, got %+v %v", res, err)
	}
	rows, err := st.ListAudit(context.Background(), store.AuditFilter{Action: "validate"})
	if err != nil || len(rows) != 1 || rows[0].Result != service.CodeValidation || !strings.Contains(rows[0].Params, "cap_add") {
		t.Fatalf("audit rows = %+v (%v)", rows, err)
	}
}

// A reverse proxy on the same host forwards to 127.0.0.1 with the public
// Host header; the SDK's DNS-rebinding guard must not reject that. Every
// request needs a bearer token, which a rebinding browser cannot supply.
func TestLoopbackProxyWithPublicHost(t *testing.T) {
	url, tok := setup(t)
	req, _ := http.NewRequest("POST", url, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	req.Host = "mcp.example.com"
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2025-06-18")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		t.Fatal("loopback request with a public Host header was rejected")
	}
}

// Logs are sent once, as text: the SDK would otherwise add a structured copy
// and a JSON-escaped text copy, multiplying the 256 KB cap on the wire.
func TestLogsToolSendsLogsOnceAsText(t *testing.T) {
	url, tok, _, docker := setupWithStore(t)
	cs, err := connect(t, url, tok)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	ctx := context.Background()
	if _, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "deploy", Arguments: map[string]any{
		"app": "shop", "stack": map[string]any{"services": map[string]any{"web": map[string]any{"image": "a:1"}}}}}); err != nil {
		t.Fatal(err)
	}
	docker.LogsOut = strings.Repeat("<line> & more\n", 1000)
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "logs", Arguments: map[string]any{"app": "shop", "service": "web"}})
	if err != nil || res.IsError {
		t.Fatalf("logs: %+v %v", res, err)
	}
	if res.StructuredContent != nil || len(res.Content) != 1 {
		t.Fatalf("want one text content and no structured copy, got %d contents, structured=%v", len(res.Content), res.StructuredContent != nil)
	}
	if tc, ok := res.Content[0].(*mcp.TextContent); !ok || tc.Text != docker.LogsOut {
		t.Fatalf("text content differs from the logs")
	}
}

// Every tool works over MCP, which pins the input schemas inferred from the
// Go types (spec §19).
func TestEveryToolOverStreamableHTTP(t *testing.T) {
	url, tok := setup(t)
	cs, err := connect(t, url, tok)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	ctx := context.Background()
	stack := map[string]any{"services": map[string]any{"web": map[string]any{"image": "nginx:1.27.0"}}}
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"whoami", map[string]any{}},
		{"list_templates", map[string]any{}},
		{"validate", map[string]any{"app": "shop", "stack": stack}},
		{"deploy", map[string]any{"app": "shop", "stack": stack}},
		{"list", map[string]any{}},
		{"logs", map[string]any{"app": "shop", "service": "web", "tail": 10}},
		{"restart", map[string]any{"app": "shop"}},
		{"restart", map[string]any{"app": "shop", "service": "web"}},
		{"stop", map[string]any{"app": "shop", "service": "web"}},
		{"stop", map[string]any{"app": "shop"}},
		{"delete", map[string]any{"app": "shop", "confirm": "shop"}},
	} {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: c.tool, Arguments: c.args})
		if err != nil || res.IsError {
			t.Fatalf("%s %v: %v %+v", c.tool, c.args, err, res)
		}
	}
}

// A token without a gateway principal (e.g. from another verifier) is not
// authenticated.
func TestPrincipalRequiresGatewayPrincipal(t *testing.T) {
	for name, req := range map[string]*mcp.CallToolRequest{
		"no extra":        {},
		"no token":        {Extra: &mcp.RequestExtra{}},
		"no principal":    {Extra: &mcp.RequestExtra{TokenInfo: &sdkauth.TokenInfo{Extra: map[string]any{}}}},
		"wrong principal": {Extra: &mcp.RequestExtra{TokenInfo: &sdkauth.TokenInfo{Extra: map[string]any{"principal": "alice"}}}},
	} {
		if _, err := principal(req); err == nil || !strings.Contains(err.Error(), "unauthorized") {
			t.Errorf("%s: err = %v, want unauthorized", name, err)
		}
	}
	want := service.Principal{TokenID: 7}
	got, err := principal(&mcp.CallToolRequest{Extra: &mcp.RequestExtra{TokenInfo: &sdkauth.TokenInfo{Extra: map[string]any{"principal": want}}}})
	if err != nil || got.TokenID != 7 {
		t.Fatalf("principal = %+v, %v", got, err)
	}
}

// A tool call that reaches the server without the bearer middleware (here:
// an in-memory transport) is refused before the service runs.
func TestToolsWithoutPrincipalAreUnauthorized(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := service.New(st, &servicetest.FakeCompose{}, servicetest.NewFakeDocker(), t.TempDir())
	serverT, clientT := mcp.NewInMemoryTransports()
	ss, err := New(svc, "test").Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"whoami", map[string]any{}},
		{"logs", map[string]any{"app": "shop", "service": "web"}},
	} {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: c.tool, Arguments: c.args})
		if err != nil || !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, `"code":"unauthorized"`) {
			t.Fatalf("%s without principal: %v %+v, want an unauthorized tool error", c.tool, err, res)
		}
	}
	if rows, _ := st.ListAudit(ctx, store.AuditFilter{}); len(rows) != 0 {
		t.Fatalf("the service ran: audit rows = %+v", rows)
	}
}
