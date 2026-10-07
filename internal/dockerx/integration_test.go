//go:build integration

package dockerx

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestIntegrationRemoveVolume(t *testing.T) {
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	name := strings.Repeat("d9", 32) // 64 hex characters, like an anonymous volume
	if out, err := exec.Command("docker", "volume", "create", name).CombinedOutput(); err != nil {
		t.Fatalf("create: %v %s", err, out)
	}
	t.Cleanup(func() { exec.Command("docker", "volume", "rm", name).Run() })
	if !IsAnonymousVolume(name) {
		t.Fatal("test volume name must look anonymous")
	}
	ctx := context.Background()
	if err := c.RemoveVolume(ctx, name); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if out, _ := exec.Command("docker", "volume", "inspect", name).CombinedOutput(); !strings.Contains(string(out), "no such volume") {
		t.Fatalf("volume still exists: %s", out)
	}
	if err := c.RemoveVolume(ctx, name); err != nil {
		t.Fatalf("removing a missing volume must not fail: %v", err)
	}
}

func TestIntegrationAddressPoolsConfigured(t *testing.T) {
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddressPoolsConfigured(context.Background()); err != nil {
		t.Fatalf("info: %v", err)
	}
}

func TestIntegrationRemoveAppNetwork(t *testing.T) {
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	mk := func(name, project string) {
		t.Helper()
		if out, err := exec.Command("docker", "network", "create", "--label", "com.docker.compose.project="+project, name).CombinedOutput(); err != nil {
			t.Fatalf("create %s: %v %s", name, err, out)
		}
		t.Cleanup(func() { exec.Command("docker", "network", "rm", name).Run() })
	}
	exists := func(name string) bool { return exec.Command("docker", "network", "inspect", name).Run() == nil }
	mk("dgrev-itest-own", "dg-itest-own")
	mk("dgrev-itest-foreign", "someone-else")
	if err := c.RemoveAppNetwork(ctx, "dgrev-itest-own", "dg-itest-own"); err != nil || exists("dgrev-itest-own") {
		t.Fatalf("own network not removed (err %v)", err)
	}
	if err := c.RemoveAppNetwork(ctx, "dgrev-itest-foreign", "dg-itest-own"); err != nil || !exists("dgrev-itest-foreign") {
		t.Fatalf("network of another project touched (err %v)", err)
	}
	if err := c.RemoveAppNetwork(ctx, "dgrev-itest-missing", "dg-itest-own"); err != nil {
		t.Fatalf("missing network: %v", err)
	}
}

func TestIntegrationDefaultRuntimeAllowed(t *testing.T) {
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	rt, err := c.DefaultRuntime(context.Background())
	if err != nil || !RuntimeAllowed(rt, nil) {
		t.Fatalf("default runtime %q (%v)", rt, err)
	}
}

func TestIntegrationContainerQueriesAreScoped(t *testing.T) {
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	mk := func(name string, labels ...string) {
		t.Helper()
		args := []string{"create", "--name", name}
		for _, l := range labels {
			args = append(args, "--label", l)
		}
		args = append(args, "nginxinc/nginx-unprivileged:1.27-alpine")
		if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
			t.Fatalf("create %s: %v %s", name, err, out)
		}
		t.Cleanup(func() { exec.Command("docker", "rm", "-f", name).Run() })
	}
	mk("dgrev-scope-a", "deploy-gateway.workspace=dgrevscope", "deploy-gateway.app=a", "deploy-gateway.managed=true")
	mk("dgrev-scope-b", "deploy-gateway.workspace=dgrevscope", "deploy-gateway.app=b", "deploy-gateway.managed=true")
	mk("dgrev-scope-other", "deploy-gateway.workspace=dgrevother", "deploy-gateway.app=a")
	mk("dgrev-scope-plain")
	names := func(cs []ContainerInfo, err error) []string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, c := range cs {
			if strings.HasPrefix(c.Name, "dgrev-scope-") {
				out = append(out, c.Name)
			}
		}
		slices.Sort(out)
		return out
	}
	ctx := context.Background()
	if got := names(c.ProjectContainers(ctx, "dgrevscope", "a")); !slices.Equal(got, []string{"dgrev-scope-a"}) {
		t.Errorf("ProjectContainers = %v", got)
	}
	if got := names(c.WorkspaceContainers(ctx, "dgrevscope")); !slices.Equal(got, []string{"dgrev-scope-a", "dgrev-scope-b"}) {
		t.Errorf("WorkspaceContainers = %v", got)
	}
	if got := names(c.ManagedContainers(ctx)); !slices.Equal(got, []string{"dgrev-scope-a", "dgrev-scope-b"}) {
		t.Errorf("ManagedContainers = %v", got)
	}
}
