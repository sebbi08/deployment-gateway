package service_test

import (
	"testing"

	"github.com/sebbi08/deployment-gateway/internal/dockerx"
	"github.com/sebbi08/deployment-gateway/internal/store"
)

// An app left "pending" by a gateway that died mid-deploy may run
// containers that never passed the runtime check.
func TestRecoverInterruptedChecksPendingApps(t *testing.T) {
	for name, tc := range map[string]struct {
		tamper     bool
		wantResult string
		wantLeft   int
	}{
		"clean":     {false, "interrupted", 2},
		"violating": {true, "security_violation", 0},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			p := h.workspace("alice", 20000, 20099)
			deployExample(t, h, p)
			app, _ := h.st.GetApp(h.ctx, p.Workspace.ID, "shop")
			if err := h.st.RecordDeploy(h.ctx, app.ID, "pending"); err != nil {
				t.Fatal(err)
			}
			if tc.tamper {
				h.docker.MutateFacts("alice-shop-web", func(f *dockerx.Facts) { f.Privileged = true })
			}
			if err := h.svc.RecoverInterrupted(h.ctx); err != nil {
				t.Fatal(err)
			}
			app, _ = h.st.GetApp(h.ctx, p.Workspace.ID, "shop")
			if app.LastDeployResult != tc.wantResult || len(h.docker.Containers) != tc.wantLeft {
				t.Fatalf("result %q with %d containers, want %q with %d", app.LastDeployResult, len(h.docker.Containers), tc.wantResult, tc.wantLeft)
			}
			rows, _ := h.st.ListAudit(h.ctx, store.AuditFilter{Action: "recover_interrupted"})
			if len(rows) != 1 || rows[0].Workspace != "alice" {
				t.Fatalf("audit rows = %+v", rows)
			}
		})
	}
}
