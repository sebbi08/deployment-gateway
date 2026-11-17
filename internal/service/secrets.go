package service

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sebbi08/deployment-gateway/internal/names"
	"github.com/sebbi08/deployment-gateway/internal/render"
	"github.com/sebbi08/deployment-gateway/internal/stack"
	"github.com/sebbi08/deployment-gateway/internal/store"
)

// minMaskLen is the shortest secret value masked in agent-facing output;
// shorter values would mask ordinary words.
const minMaskLen = 6

// requestSecrets registers every referenced secret name the workspace does
// not know yet as requested (spec §4.2). It is the only side effect of
// validate and runs before plan, which stays read-only. Names and the app
// name are checked here because plan has not validated the stack yet;
// invalid ones are reported by plan.
func (s *Service) requestSecrets(ctx context.Context, ws store.Workspace, app string, st *stack.Stack) error {
	if !names.ValidApp(app) {
		return nil
	}
	var valid []string
	for _, n := range stack.SecretNames(st) {
		if names.ValidSecret(n) {
			valid = append(valid, n)
		}
	}
	if len(valid) == 0 {
		return nil
	}
	return s.Store.RequestSecrets(ctx, ws.ID, valid, app)
}

// resolvedSecrets are the values (service -> env key -> value) and refs of
// a stack's secret references.
type resolvedSecrets struct {
	values map[string]map[string]string
	refs   []store.SecretRefInput
}

// resolveSecrets maps every secret reference of the stack to its current
// value. Unset (requested) or unknown names become field errors.
func (s *Service) resolveSecrets(ctx context.Context, ws store.Workspace, st *stack.Stack) (resolvedSecrets, stack.Errors, error) {
	res := resolvedSecrets{values: map[string]map[string]string{}}
	if len(stack.SecretNames(st)) == 0 {
		return res, nil, nil
	}
	values, err := s.Store.SecretValues(ctx, ws.ID)
	if err != nil {
		return resolvedSecrets{}, nil, err
	}
	var known map[string]bool
	var errs stack.Errors
	for _, svc := range stack.ServiceNames(st) {
		secrets := st.Services[svc].Secrets
		for _, key := range slices.Sorted(maps.Keys(secrets)) {
			name := secrets[key]
			if v, ok := values[name]; ok {
				if res.values[svc] == nil {
					res.values[svc] = map[string]string{}
				}
				res.values[svc][key] = v
				res.refs = append(res.refs, store.SecretRefInput{Service: svc, EnvKey: key, Secret: name})
				continue
			}
			if known == nil {
				all, err := s.Store.ListSecrets(ctx, ws.ID)
				if err != nil {
					return resolvedSecrets{}, nil, err
				}
				known = map[string]bool{}
				for _, x := range all {
					known[x.Name] = true
				}
			}
			msg := fmt.Sprintf("secret %q has no value yet; ask the operator to set it in the deploy-gateway admin UI (workspace %q, tab Secrets)", name, ws.Name)
			if !known[name] {
				msg = fmt.Sprintf("secret %q does not exist and the workspace already holds %d secrets; ask the operator to remove unused ones", name, store.MaxSecretsPerWorkspace)
			}
			errs = append(errs, stack.FieldError{Field: "services." + svc + ".secrets." + key, Message: msg})
		}
	}
	return res, errs, nil
}

// ensureEnvFiles runs before pull: the next compose file may reference env
// files of new services, which compose must be able to load. Missing files
// are created empty; existing ones are never touched, so a deploy that fails
// before commit leaves the running deployment's env files as they were. The
// folder and files stay root-only (never FILE_OWNER).
func (s *Service) ensureEnvFiles(ws, app string, st *stack.Stack) error {
	dir := s.secretsDir(ws, app)
	if err := ensurePrivateDir(dir); err != nil {
		return err
	}
	for _, svc := range stack.ServiceNames(st) {
		if len(st.Services[svc].Secrets) == 0 {
			continue
		}
		f, err := os.OpenFile(render.EnvFilePath(dir, svc), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	return syncDir(dir)
}

// writeEnvFiles runs at commit: it writes the env file of every service with
// secrets from the re-planned values (temp file, fsync, rename) and removes
// every other file in the folder: env files of services without secrets and
// temp files left by a crash. Values containing NUL, CR or LF
// are refused before anything is written: with format: raw a line break
// would inject another variable.
func (s *Service) writeEnvFiles(ws, app string, pl *planned) error {
	for _, svc := range slices.Sorted(maps.Keys(pl.secretValues)) {
		for _, key := range slices.Sorted(maps.Keys(pl.secretValues[svc])) {
			if strings.ContainsAny(pl.secretValues[svc][key], "\x00\r\n") {
				return fmt.Errorf("secret value for services.%s.secrets.%s contains NUL, CR or LF; refusing to write the env file", svc, key)
			}
		}
	}
	dir := s.secretsDir(ws, app)
	if err := ensurePrivateDir(dir); err != nil {
		return err
	}
	want := map[string]bool{}
	for svc, values := range pl.secretValues {
		if len(values) == 0 {
			continue
		}
		path := render.EnvFilePath(dir, svc)
		want[filepath.Base(path)] = true
		if err := writePrivateFile(dir, path, render.EnvFile(values)); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || want[e.Name()] {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return syncDir(dir)
}

// ensurePrivateDir creates dir as 0700 and resets its mode if it exists.
func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}

// writePrivateFile atomically replaces path with a 0600 file holding data.
func writePrivateFile(dir, path string, data []byte) error {
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// masker returns a function that replaces every secret value of the
// workspace that is at least minMaskLen bytes long with *** (spec §4.4). It
// is best effort, not a security boundary. Apply it before sanitize so
// truncation cannot cut a value in half.
func (s *Service) masker(ctx context.Context, wsID int64) func(string) string {
	values, err := s.Store.SecretValues(context.WithoutCancel(ctx), wsID)
	if err != nil {
		s.Logger.Error("loading secret values for masking failed", "err", err)
	}
	return maskValues(slices.Collect(maps.Values(values)))
}

// maskValues replaces the given values (longest first, so a value that
// contains another is masked whole) with ***.
func maskValues(values []string) func(string) string {
	var long []string
	for _, v := range values {
		if len(v) >= minMaskLen {
			long = append(long, v)
		}
	}
	if len(long) == 0 {
		return func(s string) string { return s }
	}
	slices.SortFunc(long, func(a, b string) int { return cmp.Or(cmp.Compare(len(b), len(a)), strings.Compare(a, b)) })
	pairs := make([]string, 0, 2*len(long))
	for _, v := range slices.Compact(long) {
		pairs = append(pairs, v, "***")
	}
	r := strings.NewReplacer(pairs...)
	return r.Replace
}

// secretEnv returns the sorted env keys of a service that come from secrets.
func secretEnv(st *stack.Stack, svc string) []string {
	if st == nil || len(st.Services[svc].Secrets) == 0 {
		return nil
	}
	return slices.Sorted(maps.Keys(st.Services[svc].Secrets))
}
