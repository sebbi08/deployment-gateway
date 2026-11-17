package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/sebbi08/deployment-gateway/internal/names"
	"github.com/sebbi08/deployment-gateway/internal/store"
)

// maxSecretValue bounds one secret value in bytes.
const maxSecretValue = 8192

// SecretView is one workspace secret as the admin sees it. It never carries
// the value.
type SecretView struct {
	Name        string      `json:"name"`
	State       string      `json:"state"` // "set" | "requested"
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
	RequestedAt *time.Time  `json:"requested_at,omitempty"`
	RequestedBy string      `json:"requested_by,omitempty"`
	UsedBy      []SecretUse `json:"used_by"`
}

// SecretUse is one env var of one service bound to a secret.
type SecretUse struct {
	App        string `json:"app"`
	Service    string `json:"service"`
	EnvKey     string `json:"env_key"`
	Stale      bool   `json:"stale"`
	ApplyState string `json:"apply_state"`
	ApplyError string `json:"apply_error"`
}

func (s *Service) AdminSecrets(ctx context.Context, w store.Workspace) ([]SecretView, error) {
	secrets, err := s.Store.ListSecrets(ctx, w.ID)
	if err != nil {
		return nil, err
	}
	refs, err := s.Store.ListSecretRefs(ctx, w.ID)
	if err != nil {
		return nil, err
	}
	apps, err := s.Store.ListApps(ctx, w.ID)
	if err != nil {
		return nil, err
	}
	byID := map[int64]store.App{}
	for _, a := range apps {
		byID[a.ID] = a
	}
	mask := s.masker(ctx, w.ID)
	uses := map[string][]SecretUse{}
	for _, r := range refs {
		a := byID[r.AppID]
		uses[r.Secret] = append(uses[r.Secret], SecretUse{App: r.App, Service: r.Service, EnvKey: r.EnvKey, Stale: r.Stale,
			ApplyState: a.SecretApplyState, ApplyError: mask(a.SecretApplyError)})
	}
	out := make([]SecretView, 0, len(secrets))
	for _, sec := range secrets {
		v := SecretView{Name: sec.Name, State: "requested", CreatedAt: sec.CreatedAt, UpdatedAt: sec.UpdatedAt,
			RequestedAt: sec.RequestedAt, RequestedBy: sec.RequestedBy, UsedBy: uses[sec.Name]}
		if sec.Set {
			v.State = "set"
		}
		if v.UsedBy == nil {
			v.UsedBy = []SecretUse{}
		}
		out = append(out, v)
	}
	return out, nil
}

// validateSecret checks name and value. Messages never echo the value.
func validateSecret(name, value string) error {
	if !names.ValidSecret(name) {
		return validationError("name", "secret name is invalid")
	}
	if len(value) < 1 || len(value) > maxSecretValue {
		return validationError("value", fmt.Sprintf("value must be 1-%d bytes", maxSecretValue))
	}
	if strings.ContainsAny(value, "\x00\r\n") {
		return validationError("value", "value must not contain NUL, CR or LF")
	}
	return nil
}

func (s *Service) CreateSecret(ctx context.Context, actor string, w store.Workspace, name, value string) (err error) {
	defer s.adminTrack(ctx, actor, w.Name, "secret_create", map[string]string{"name": name}, &err)()
	if err = validateSecret(name, value); err != nil {
		return err
	}
	mu := s.wsMutex(w.Name)
	mu.Lock()
	defer mu.Unlock()
	all, err := s.Store.ListSecrets(ctx, w.ID)
	if err != nil {
		return err
	}
	if len(all) >= store.MaxSecretsPerWorkspace {
		return validationError("name", fmt.Sprintf("this workspace already holds %d secrets", store.MaxSecretsPerWorkspace))
	}
	err = s.Store.CreateSecret(ctx, w.ID, name, value)
	if errors.Is(err, store.ErrExists) {
		return &Error{Code: CodeConflict, Message: "secret already exists"}
	}
	return err
}

// distinctApps returns the apps of refs: ids in order, names sorted.
func distinctApps(refs []store.SecretRef) ([]int64, []string) {
	var ids []int64
	apps := []string{}
	for _, r := range refs {
		if !slices.Contains(ids, r.AppID) {
			ids = append(ids, r.AppID)
			apps = append(apps, r.App)
		}
	}
	slices.Sort(apps)
	return ids, apps
}

// SetSecret sets or replaces a value and queues a re-apply of the apps using
// it. The workspace mutex covers both steps: the apply job's stale-clearing
// relies on it.
func (s *Service) SetSecret(ctx context.Context, actor string, w store.Workspace, name, value string) (affected []string, err error) {
	defer s.adminTrack(ctx, actor, w.Name, "secret_set", map[string]string{"name": name}, &err)()
	if err = validateSecret(name, value); err != nil {
		return nil, err
	}
	mu := s.wsMutex(w.Name)
	mu.Lock()
	defer mu.Unlock()
	refs, err := s.Store.SetSecretValue(ctx, w.ID, name, value)
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFoundError("secret")
	}
	if err != nil {
		return nil, err
	}
	ids, affected := distinctApps(refs)
	s.enqueueApply(w.Name, ids)
	return affected, nil
}

// ReapplySecret queues the apps whose ref to the secret is still stale.
func (s *Service) ReapplySecret(ctx context.Context, actor string, w store.Workspace, name string) (affected []string, err error) {
	defer s.adminTrack(ctx, actor, w.Name, "secret_apply_request", map[string]string{"name": name}, &err)()
	mu := s.wsMutex(w.Name)
	mu.Lock()
	defer mu.Unlock()
	all, err := s.Store.ListSecrets(ctx, w.ID)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(all, func(x store.Secret) bool { return x.Name == name }) {
		return nil, notFoundError("secret")
	}
	refs, err := s.Store.ListSecretRefs(ctx, w.ID)
	if err != nil {
		return nil, err
	}
	var stale []store.SecretRef
	for _, r := range refs {
		if r.Secret == name && r.Stale {
			stale = append(stale, r)
		}
	}
	ids, affected := distinctApps(stale)
	s.enqueueApply(w.Name, ids)
	return affected, nil
}

func (s *Service) DeleteSecret(ctx context.Context, actor string, w store.Workspace, name string) (err error) {
	defer s.adminTrack(ctx, actor, w.Name, "secret_delete", map[string]string{"name": name}, &err)()
	mu := s.wsMutex(w.Name)
	mu.Lock()
	defer mu.Unlock()
	err = s.Store.DeleteSecret(ctx, w.ID, name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return notFoundError("secret")
	case errors.Is(err, store.ErrInUse):
		refs, lerr := s.Store.ListSecretRefs(ctx, w.ID)
		if lerr != nil {
			return lerr
		}
		var used []store.SecretRef
		for _, r := range refs {
			if r.Secret == name {
				used = append(used, r)
			}
		}
		_, apps := distinctApps(used)
		return &Error{Code: CodeConflict, Message: "secret is used by apps: " + strings.Join(apps, ", ")}
	}
	return err
}
