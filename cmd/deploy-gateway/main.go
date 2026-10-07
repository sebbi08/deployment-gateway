// Command deploy-gateway runs the deployment gateway (see README.md).
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/sebbi08/deployment-gateway/internal/admin"
	"github.com/sebbi08/deployment-gateway/internal/agentauth"
	"github.com/sebbi08/deployment-gateway/internal/api"
	"github.com/sebbi08/deployment-gateway/internal/auth"
	"github.com/sebbi08/deployment-gateway/internal/config"
	"github.com/sebbi08/deployment-gateway/internal/dockerx"
	"github.com/sebbi08/deployment-gateway/internal/mcpserver"
	"github.com/sebbi08/deployment-gateway/internal/runner"
	"github.com/sebbi08/deployment-gateway/internal/service"
	"github.com/sebbi08/deployment-gateway/internal/store"
	"github.com/sebbi08/deployment-gateway/internal/webui"
)

var version = "dev"

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "hash-password":
		os.Exit(hashPassword(os.Stdin, os.Stdout, os.Stderr))
	case "serve":
		cfg, err := config.Load(os.Getenv)
		if err != nil {
			logger.Error("invalid configuration", "err", err)
			os.Exit(2)
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := serve(ctx, cfg, logger); err != nil {
			logger.Error("fatal", "err", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: deploy-gateway [serve|hash-password]")
		os.Exit(2)
	}
}

// hashPassword reads one line from in and prints its argon2id hash.
func hashPassword(in io.Reader, out, errOut io.Writer) int {
	fmt.Fprint(errOut, "Password (min. 12 characters): ")
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		fmt.Fprintln(errOut, err)
		return 1
	}
	pw := strings.TrimRight(line, "\r\n")
	if len(pw) < 12 {
		fmt.Fprintln(errOut, "password must be at least 12 characters")
		return 1
	}
	h, err := auth.HashPassword(pw)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	fmt.Fprintln(out, h)
	return 0
}

type mountInspector interface {
	MountSource(ctx context.Context, containerID, destination string) (string, bool, error)
}

// checkSameMount enforces that WORKSPACES_ROOT is bind-mounted from the
// identical host path, because the Docker daemon resolves bind sources on
// the host. Running directly on the host passes trivially.
//
// containerized is detected independently (/.dockerenv); when true, failing
// to find our own container is an error rather than "running on the host".
func checkSameMount(ctx context.Context, m mountInspector, self, root string, containerized bool) error {
	src, inContainer, err := m.MountSource(ctx, self, root)
	if err != nil {
		return fmt.Errorf("inspect own container %q: %w", self, err)
	}
	if !inContainer {
		if containerized {
			return fmt.Errorf("running in a container but cannot find own container %q; leave the container hostname at its default or set SELF_CONTAINER_ID", self)
		}
		return nil
	}
	if src != root {
		return fmt.Errorf("WORKSPACES_ROOT %s must be bind-mounted from the identical host path (found %q)", root, src)
	}
	return nil
}

type pinger interface{ Ping(context.Context) error }

func agentHandler(svc *service.Service, authn *agentauth.Authenticator, p pinger) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/mcp", agentauth.ReadBodyFirst(mcpserver.Handler(mcpserver.New(svc, version), authn)))
	mux.Handle("/api/v1/", agentauth.ReadBodyFirst((&api.Handler{Svc: svc, Auth: authn}).Routes()))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := p.Ping(r.Context()); err != nil {
			http.Error(w, "degraded: docker unreachable", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ok")
	})
	return mux
}

func serve(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	root, err := filepath.EvalSymlinks(cfg.WorkspacesRoot)
	if err != nil {
		return fmt.Errorf("WORKSPACES_ROOT: %w", err)
	}
	if root != cfg.WorkspacesRoot {
		return fmt.Errorf("WORKSPACES_ROOT must not contain symlinks (resolves to %s)", root)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, "deploy-gateway.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	dc, err := dockerx.New()
	if err != nil {
		return err
	}
	if err := dc.Ping(ctx); err != nil {
		return fmt.Errorf("docker unreachable: %w", err)
	}
	// SELF_CONTAINER_ID (optional) names this container when its hostname
	// was overridden; defaults to the hostname (= short container ID).
	self := os.Getenv("SELF_CONTAINER_ID")
	if self == "" {
		if self, err = os.Hostname(); err != nil {
			return fmt.Errorf("hostname: %w", err)
		}
	}
	_, statErr := os.Stat("/.dockerenv")
	if err := checkSameMount(ctx, dc, self, root, statErr == nil); err != nil {
		return err
	}
	if out, err := exec.CommandContext(ctx, cfg.ComposeBin, "version", "--short").CombinedOutput(); err != nil {
		return fmt.Errorf("compose binary %s not usable: %v: %s", cfg.ComposeBin, err, out)
	}

	run := runner.New(runner.Options{Bin: cfg.ComposeBin, DockerConfig: cfg.DockerConfig, DockerHost: cfg.DockerHost,
		Timeout: cfg.DeployTimeout, Concurrency: cfg.DeployConcurrency})
	svc := service.New(st, run, dc, root)
	svc.Logger = logger
	if cfg.FileOwnerSet {
		svc.FileOwner = &service.Owner{UID: cfg.FileUID, GID: cfg.FileGID}
	}
	warnMissingNetworks(ctx, st, dc, logger)
	if orphans, err := svc.Orphans(ctx); err == nil && len(orphans) > 0 {
		logger.Warn("orphaned gateway containers found; see the admin UI", "count", len(orphans))
	}

	warnUntrustedProxies(cfg, logger)
	warnBroadAdminProxies(cfg, logger)
	warnDefaultAddressPools(ctx, dc, logger)
	authn := newAuthenticator(st, cfg, logger)
	adminSrv := admin.New(admin.Options{Svc: svc, Store: st, PasswordHash: cfg.AdminPasswordHash,
		TrustedProxies: cfg.AdminTrustedProxies, ForwardedProxies: cfg.TrustedProxies, UserHeader: cfg.AdminUserHeader, UI: webui.FS(), Logger: logger})
	servers := []*http.Server{
		{Addr: cfg.AgentListen, Handler: agentHandler(svc, authn, dc), ReadHeaderTimeout: 10 * time.Second,
			WriteTimeout: cfg.DeployTimeout + 2*time.Minute, IdleTimeout: 2 * time.Minute},
		{Addr: cfg.AdminListen, Handler: adminSrv.Handler(), ReadHeaderTimeout: 10 * time.Second,
			WriteTimeout: cfg.DeployTimeout + 2*time.Minute, IdleTimeout: 2 * time.Minute},
	}
	go pruneAudit(ctx, st, logger)

	errc := make(chan error, len(servers))
	for _, srv := range servers {
		go func() {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
		}()
	}
	mode := "password"
	if cfg.ProxyMode() {
		mode = "proxy"
	}
	logger.Info("deploy-gateway started", "version", version, "agent", cfg.AgentListen, "admin", cfg.AdminListen, "admin_auth", mode, "root", root)

	select {
	case <-ctx.Done():
	case err := <-errc:
		return err
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, srv := range servers {
		srv.Shutdown(shutdownCtx)
	}
	return nil
}

// Per-token limits: at most 8 concurrent calls (a deploy holds its slot until
// it finishes) and a token bucket of 30 calls refilled at 5 per second.
const (
	maxInFlightPerToken = 8
	rateBurstPerToken   = 30
	ratePerTokenPerSec  = 5
)

func newAuthenticator(st *store.Store, cfg config.Config, logger *slog.Logger) *agentauth.Authenticator {
	return &agentauth.Authenticator{Store: st, Limiter: auth.NewLimiter(10, time.Minute, 15*time.Minute),
		TrustedProxies: cfg.TrustedProxies, Logger: logger, MaxInFlight: maxInFlightPerToken,
		RateBurst: rateBurstPerToken, RatePerSecond: ratePerTokenPerSec}
}

type addressPools interface {
	AddressPoolsConfigured(context.Context) (bool, error)
}

// warnDefaultAddressPools flags a daemon without default-address-pools:
// every app gets its own network, and the built-in pools allow only about
// 31 networks host-wide (shared with the host's own stacks).
func warnDefaultAddressPools(ctx context.Context, p addressPools, logger *slog.Logger) {
	configured, err := p.AddressPoolsConfigured(ctx)
	if err != nil {
		logger.Warn("cannot read the Docker daemon's address pools", "err", err)
		return
	}
	if !configured {
		logger.Warn(`Docker uses its built-in address pools (about 31 networks host-wide) and every app needs one network; configure "default-address-pools" in daemon.json, e.g. {"base":"10.201.0.0/16","size":24}`)
	}
}

// warnBroadAdminProxies flags address ranges in ADMIN_TRUSTED_PROXIES in
// proxy mode: every container that gets an address in the range can send
// the identity header and act as admin.
func warnBroadAdminProxies(cfg config.Config, logger *slog.Logger) {
	if !cfg.ProxyMode() {
		return
	}
	for _, p := range cfg.AdminTrustedProxies {
		if p.Bits() < p.Addr().BitLen() {
			logger.Warn("ADMIN_TRUSTED_PROXIES contains a range: every container with an address in it can claim an admin identity; use the reverse proxy's pinned single IP or a network only the proxy and the gateway share",
				"prefix", p.String())
		}
	}
}

// warnUntrustedProxies flags a missing TRUSTED_PROXIES: behind a reverse
// proxy every agent then shares the proxy's address in the audit log and
// rate limiter.
func warnUntrustedProxies(cfg config.Config, logger *slog.Logger) {
	if len(cfg.TrustedProxies) == 0 {
		logger.Warn("TRUSTED_PROXIES is empty: behind a reverse proxy all agents are logged and rate-limited under the proxy's address; set it to the proxy's IP")
	}
}

func warnMissingNetworks(ctx context.Context, st *store.Store, dc *dockerx.Client, logger *slog.Logger) {
	templates, err := st.ListTemplates(ctx)
	if err != nil {
		logger.Warn("cannot list templates", "err", err)
		return
	}
	for _, t := range templates {
		for _, n := range t.Networks {
			if ok, err := dc.NetworkExists(ctx, n); err != nil || !ok {
				logger.Warn("template network missing", "template", t.Name, "network", n, "err", err)
			}
		}
	}
}

func pruneAudit(ctx context.Context, st *store.Store, logger *slog.Logger) {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		if days, err := st.AuditRetentionDays(ctx); err != nil {
			logger.Warn("audit retention lookup failed", "err", err)
		} else if n, err := st.PruneAudit(ctx, time.Now().AddDate(0, 0, -days)); err != nil {
			logger.Warn("audit prune failed", "err", err)
		} else if n > 0 {
			logger.Info("pruned audit log", "rows", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
