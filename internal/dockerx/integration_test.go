//go:build integration

package dockerx

import (
	"context"
	"os/exec"
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
