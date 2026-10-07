package service

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sebbi08/deployment-gateway/internal/auth"
	"github.com/sebbi08/deployment-gateway/internal/dockerx"
	"github.com/sebbi08/deployment-gateway/internal/names"
	"github.com/sebbi08/deployment-gateway/internal/store"
	"github.com/sebbi08/deployment-gateway/internal/tmpl"
)

type PortSettings struct {
	Start  int    `json:"start"`
	End    int    `json:"end"`
	BindIP string `json:"bind_ip"`
}

// validatePortSettings validates ps and canonicalises ps.BindIP (an
// IPv4-mapped IPv6 address is stored as plain IPv4) so the runtime check
// compares against the form Docker reports.
func (s *Service) validatePortSettings(ctx context.Context, ps *PortSettings, excludeID int64) error {
	if !(ps.Start == 0 && ps.End == 0) && (ps.Start < 1024 || ps.End > 65535 || ps.Start > ps.End) {
		return validationError("ports", "port range must be within 1024-65535 with start <= end (or 0-0 to disable publishing)")
	}
	addr, err := netip.ParseAddr(ps.BindIP)
	if err != nil {
		return validationError("bind_ip", "bind IP must be an IP address")
	}
	ps.BindIP = addr.Unmap().String()
	if ps.Start > 0 {
		name, err := s.Store.OverlappingWorkspace(ctx, ps.Start, ps.End, excludeID)
		if err != nil {
			return err
		}
		if name != "" {
			return validationError("ports", fmt.Sprintf("port range overlaps workspace %q", name))
		}
	}
	return nil
}

func (s *Service) CreateWorkspace(ctx context.Context, actor, name string, q store.Quota, ps PortSettings) (w store.Workspace, err error) {
	defer s.adminTrack(ctx, actor, name, "create_workspace", map[string]any{"quota": q, "ports": ps}, &err)()
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	if !names.ValidWorkspace(name) {
		return w, validationError("name", "workspace name must match ^[a-z][a-z0-9]{1,15}$")
	}
	if err = q.Validate(); err != nil {
		return w, validationError("quota", err.Error())
	}
	if err = s.validatePortSettings(ctx, &ps, 0); err != nil {
		return w, err
	}
	if err = os.MkdirAll(s.workspaceDir(name), 0o750); err != nil {
		return w, err
	}
	if err = s.own(s.workspaceDir(name)); err != nil {
		return w, err
	}
	w, err = s.Store.CreateWorkspace(ctx, store.Workspace{Name: name, Quota: q, PortRangeStart: ps.Start, PortRangeEnd: ps.End, PortBindIP: ps.BindIP})
	if errors.Is(err, store.ErrExists) {
		return w, validationError("name", "workspace already exists")
	}
	return w, err
}

// SaveTemplate creates (id == 0) or updates a template after structural
// validation, hostname-uniqueness validation and a Docker network check.
func (s *Service) SaveTemplate(ctx context.Context, actor string, id int64, t tmpl.Template) (newID int64, err error) {
	defer s.adminTrack(ctx, actor, "", "save_template", t, &err)()
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	all, err := s.Store.ListTemplates(ctx)
	if err != nil {
		return 0, err
	}
	var others []tmpl.Template
	for _, o := range all {
		if o.ID != id {
			others = append(others, o.Template)
		}
	}
	if err = tmpl.Validate(t, others); err != nil {
		return 0, &Error{Code: CodeValidation, Message: err.Error()}
	}
	for _, o := range all {
		if o.ID == id && o.Template.HostPattern != t.HostPattern {
			if err = s.refuseWhileHostsInUse(ctx, o.Template, "change the host pattern of"); err != nil {
				return 0, err
			}
		}
	}
	for _, n := range t.Networks {
		ok, err := s.Docker.NetworkExists(ctx, n)
		if err != nil {
			return 0, err
		}
		if !ok {
			return 0, validationError("networks", fmt.Sprintf("network %q does not exist in Docker", n))
		}
	}
	if id == 0 {
		newID, err = s.Store.CreateTemplate(ctx, t)
	} else {
		newID, err = id, s.Store.UpdateTemplate(ctx, id, t)
	}
	if errors.Is(err, store.ErrExists) {
		return 0, validationError("name", "a template with this name already exists")
	}
	if errors.Is(err, store.ErrNotFound) {
		return 0, notFoundError("template")
	}
	return newID, err
}

func (s *Service) SetGrants(ctx context.Context, actor string, w store.Workspace, templateIDs []int64) (err error) {
	defer s.adminTrack(ctx, actor, w.Name, "set_grants", templateIDs, &err)()
	return s.Store.SetGrants(ctx, w.ID, templateIDs)
}

func (s *Service) UpdateWorkspace(ctx context.Context, actor string, w store.Workspace) (err error) {
	defer s.adminTrack(ctx, actor, w.Name, "update_workspace", w, &err)()
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	// Deploys commit port claims under the same mutex; without it a claim
	// could land between the check below and the write.
	mu := s.wsMutex(w.Name)
	mu.Lock()
	defer mu.Unlock()
	if err = w.Quota.Validate(); err != nil {
		return validationError("quota", err.Error())
	}
	ps := PortSettings{Start: w.PortRangeStart, End: w.PortRangeEnd, BindIP: w.PortBindIP}
	if err = s.validatePortSettings(ctx, &ps, w.ID); err != nil {
		return err
	}
	w.PortBindIP = ps.BindIP
	claims, err := s.Store.ListPortClaims(ctx, w.ID)
	if err != nil {
		return err
	}
	stored, err := s.Store.GetWorkspace(ctx, w.ID)
	if err != nil {
		return err
	}
	for _, c := range claims {
		if w.PortRangeStart == 0 || c.HostPort < w.PortRangeStart || c.HostPort > w.PortRangeEnd {
			return validationError("ports", fmt.Sprintf("app %q publishes port %d outside the new range; redeploy or delete it first", c.App, c.HostPort))
		}
		// Running containers keep publishing on the old bind IP.
		if w.PortBindIP != stored.PortBindIP {
			return validationError("ports", fmt.Sprintf("app %q publishes port %d on %s; redeploy or delete it before changing the bind IP", c.App, c.HostPort, stored.PortBindIP))
		}
	}
	return s.Store.UpdateWorkspace(ctx, w)
}

// DeleteWorkspace revokes the workspace's tokens first, so no new agent call
// can start, then removes every app and any leftover container labelled with
// the workspace. It holds the workspace mutex throughout (prepare re-checks
// that the workspace still exists under it) and runs detached from request
// cancellation so a disconnect cannot leave it half-deleted.
func (s *Service) DeleteWorkspace(ctx context.Context, actor string, w store.Workspace) (err error) {
	defer s.adminTrack(ctx, actor, w.Name, "delete_workspace", nil, &err)()
	ctx = context.WithoutCancel(ctx)
	dir := s.workspaceDir(w.Name)
	if !names.ValidWorkspace(w.Name) || filepath.Dir(dir) != s.Root {
		return errors.New("refusing to remove unexpected path " + dir)
	}
	mu := s.wsMutex(w.Name)
	mu.Lock()
	defer mu.Unlock()
	if err = s.Store.RevokeWorkspaceTokens(ctx, w.ID); err != nil {
		return err
	}
	apps, err := s.Store.ListApps(ctx, w.ID)
	if err != nil {
		return err
	}
	for _, a := range apps {
		if err = s.deleteApp(ctx, w, a.Name); err != nil {
			return err
		}
	}
	leftovers, err := s.Docker.WorkspaceContainers(ctx, w.Name)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(leftovers))
	leftoverApps := map[string]bool{}
	for _, c := range leftovers {
		ids = append(ids, c.ID)
		leftoverApps[c.Labels[names.LabelApp]] = true
	}
	vols := s.anonymousVolumes(ctx, ids)
	for _, id := range ids {
		if err = s.Docker.RemoveContainer(ctx, id); err != nil {
			return err
		}
	}
	s.removeVolumes(ctx, vols, nil)
	for app := range leftoverApps {
		if names.ValidApp(app) {
			s.removeAppNetwork(ctx, w.Name, app)
		}
	}
	if err = os.RemoveAll(dir); err != nil {
		return err
	}
	return s.Store.DeleteWorkspace(ctx, w.ID)
}

func (s *Service) CreateToken(ctx context.Context, actor string, w store.Workspace, label string) (token string, err error) {
	defer s.adminTrack(ctx, actor, w.Name, "create_token", map[string]string{"label": label}, &err)()
	label = strings.TrimSpace(label)
	if label == "" || len(label) > 100 {
		return "", validationError("label", "label must be 1-100 characters")
	}
	var hash, prefix string
	if token, hash, prefix, err = auth.GenerateToken(); err != nil {
		return "", err
	}
	if _, err = s.Store.CreateToken(ctx, w.ID, label, hash, prefix); err != nil {
		return "", err
	}
	return token, nil
}

func (s *Service) RevokeToken(ctx context.Context, actor string, w store.Workspace, id int64) (err error) {
	defer s.adminTrack(ctx, actor, w.Name, "revoke_token", map[string]int64{"id": id}, &err)()
	t, err := s.Store.GetToken(ctx, id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && t.WorkspaceID != w.ID) {
		return notFoundError("token")
	}
	if err != nil {
		return err
	}
	return s.Store.RevokeToken(ctx, id)
}

func (s *Service) DeleteTemplate(ctx context.Context, actor string, id int64) (err error) {
	defer s.adminTrack(ctx, actor, "", "delete_template", map[string]int64{"id": id}, &err)()
	if st, gerr := s.Store.GetTemplate(ctx, id); gerr == nil {
		if err = s.refuseWhileHostsInUse(ctx, st.Template, "delete"); err != nil {
			return err
		}
	}
	err = s.Store.DeleteTemplate(ctx, id)
	if errors.Is(err, store.ErrInUse) {
		return validationError("template", "template is granted to at least one workspace; revoke the grants first")
	}
	if errors.Is(err, store.ErrNotFound) {
		return notFoundError("template")
	}
	return err
}

func (s *Service) AdminApps(ctx context.Context, w store.Workspace) ([]AppView, error) {
	return s.appViews(ctx, w)
}

func (s *Service) AdminStopApp(ctx context.Context, actor string, w store.Workspace, app string) (err error) {
	defer s.adminTrack(ctx, actor, w.Name, "stop_app", map[string]string{"app": app}, &err)()
	_, err = s.lifecycle(ctx, w, app, "", s.Compose.Stop)
	return err
}

func (s *Service) AdminDeleteApp(ctx context.Context, actor string, w store.Workspace, app string) (err error) {
	defer s.adminTrack(ctx, actor, w.Name, "delete_app", map[string]string{"app": app}, &err)()
	return s.deleteApp(ctx, w, app)
}

// Orphans are gateway-labelled containers whose workspace/app is unknown.
func (s *Service) Orphans(ctx context.Context) ([]dockerx.ContainerInfo, error) {
	keys, err := s.Store.AppKeys(ctx)
	if err != nil {
		return nil, err
	}
	all, err := s.Docker.ManagedContainers(ctx)
	if err != nil {
		return nil, err
	}
	out := []dockerx.ContainerInfo{}
	for _, c := range all {
		if !keys[c.Labels[names.LabelWorkspace]+"/"+c.Labels[names.LabelApp]] {
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *Service) RemoveOrphan(ctx context.Context, actor, id string) (err error) {
	defer s.adminTrack(ctx, actor, "", "remove_orphan", map[string]string{"id": id}, &err)()
	orphans, err := s.Orphans(ctx)
	if err != nil {
		return err
	}
	for _, o := range orphans {
		if o.ID == id {
			vols := s.anonymousVolumes(ctx, []string{id})
			if err := s.Docker.RemoveContainer(ctx, id); err != nil {
				return err
			}
			s.removeVolumes(ctx, vols, nil)
			// Removed only once the app's last container is gone.
			if ws, app := o.Labels[names.LabelWorkspace], o.Labels[names.LabelApp]; names.ValidWorkspace(ws) && names.ValidApp(app) {
				s.removeAppNetwork(ctx, ws, app)
			}
			return nil
		}
	}
	return notFoundError("orphan container")
}

func (s *Service) UpdateSettings(ctx context.Context, actor string, q store.Quota, retentionDays int) (err error) {
	defer s.adminTrack(ctx, actor, "", "update_settings", map[string]any{"default_quota": q, "audit_retention_days": retentionDays}, &err)()
	if err = q.Validate(); err != nil {
		return validationError("default_quota", err.Error())
	}
	if retentionDays < 1 || retentionDays > 3650 {
		return validationError("audit_retention_days", "audit retention must be 1-3650 days")
	}
	if err = s.Store.SetDefaultQuota(ctx, q); err != nil {
		return err
	}
	return s.Store.SetAuditRetentionDays(ctx, retentionDays)
}

// refuseWhileHostsInUse rejects a template change that could break host
// uniqueness: deployed apps keep the hosts rendered from the old pattern, so
// another workspace could later render the same host. Template suffixes
// never overlap, so a host belongs to t when it ends with t's suffix.
func (s *Service) refuseWhileHostsInUse(ctx context.Context, t tmpl.Template, action string) error {
	suffix := tmpl.HostSuffix(t.HostPattern)
	if suffix == "" {
		return nil
	}
	all, err := s.Store.AppHosts(ctx)
	if err != nil {
		return err
	}
	var apps []string
	for app, hosts := range all {
		if slices.ContainsFunc(hosts, func(h string) bool { return strings.HasSuffix(h, "."+suffix) }) {
			apps = append(apps, app)
		}
	}
	if len(apps) == 0 {
		return nil
	}
	slices.Sort(apps)
	return validationError("host_pattern", fmt.Sprintf("cannot %s template %q while apps use its hosts (%s); redeploy them without this template or delete them first",
		action, t.Name, strings.Join(apps, ", ")))
}
