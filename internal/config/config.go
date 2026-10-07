// Package config loads gateway configuration from the environment (spec §16).
package config

import (
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sebbi08/deployment-gateway/internal/auth"
)

type Config struct {
	AgentListen         string
	AdminListen         string
	DataDir             string
	WorkspacesRoot      string
	AdminPasswordHash   string
	AdminTrustedProxies []netip.Prefix
	AdminUserHeader     string
	TrustedProxies      []netip.Prefix
	DockerConfig        string
	DockerHost          string
	ComposeBin          string
	DeployTimeout       time.Duration
	DeployConcurrency   int
	// FILE_OWNER: owner of gateway-managed folders and compose files.
	FileOwnerSet bool
	FileUID      int
	FileGID      int
	// EXTRA_RUNTIMES: hardened runtimes allowed besides runc (e.g. runsc).
	ExtraRuntimes []string
}

var runtimeNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// ProxyMode is true when the built-in admin login is disabled and a trusted
// forward-auth proxy authenticates admins.
func (c Config) ProxyMode() bool { return c.AdminPasswordHash == "" }

func Load(getenv func(string) string) (Config, error) {
	or := func(key, def string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return def
	}
	c := Config{
		AgentListen:       or("AGENT_LISTEN", ":8080"),
		AdminListen:       or("ADMIN_LISTEN", ":8081"),
		DataDir:           or("DATA_DIR", "/data"),
		WorkspacesRoot:    getenv("WORKSPACES_ROOT"),
		AdminPasswordHash: getenv("ADMIN_PASSWORD_HASH"),
		AdminUserHeader:   getenv("ADMIN_USER_HEADER"),
		DockerConfig:      or("DOCKER_CONFIG", "/docker"),
		DockerHost:        or("DOCKER_HOST", "unix:///var/run/docker.sock"),
		ComposeBin:        or("COMPOSE_BIN", "/usr/local/bin/docker-compose"),
		DeployTimeout:     300 * time.Second,
		DeployConcurrency: 1,
	}
	var errs []string
	if c.WorkspacesRoot == "" || !filepath.IsAbs(c.WorkspacesRoot) || filepath.Clean(c.WorkspacesRoot) != c.WorkspacesRoot {
		errs = append(errs, "WORKSPACES_ROOT must be a clean absolute path")
	}
	var err error
	if c.AdminTrustedProxies, err = ParsePrefixes(getenv("ADMIN_TRUSTED_PROXIES")); err != nil {
		errs = append(errs, "ADMIN_TRUSTED_PROXIES: "+err.Error())
	}
	if c.TrustedProxies, err = ParsePrefixes(getenv("TRUSTED_PROXIES")); err != nil {
		errs = append(errs, "TRUSTED_PROXIES: "+err.Error())
	}
	if s := getenv("DEPLOY_TIMEOUT"); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil || d < 10*time.Second || d > time.Hour {
			errs = append(errs, "DEPLOY_TIMEOUT must be a duration between 10s and 1h")
		} else {
			c.DeployTimeout = d
		}
	}
	if s := getenv("FILE_OWNER"); s != "" {
		uid, gid, err := parseOwner(s)
		if err != nil {
			errs = append(errs, "FILE_OWNER must be numeric uid:gid (e.g. 1000:1000)")
		} else {
			c.FileOwnerSet, c.FileUID, c.FileGID = true, uid, gid
		}
	}
	for _, r := range strings.Split(getenv("EXTRA_RUNTIMES"), ",") {
		r = strings.TrimSpace(r)
		switch {
		case r == "":
		case !runtimeNameRe.MatchString(r):
			errs = append(errs, fmt.Sprintf("EXTRA_RUNTIMES: invalid runtime name %q", r))
		case strings.Contains(strings.ToLower(r), "nvidia"):
			errs = append(errs, "EXTRA_RUNTIMES: nvidia runtimes are not allowed (their hooks act on agent-controlled environment)")
		default:
			c.ExtraRuntimes = append(c.ExtraRuntimes, r)
		}
	}
	if s := getenv("DEPLOY_CONCURRENCY"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 8 {
			errs = append(errs, "DEPLOY_CONCURRENCY must be 1-8")
		} else {
			c.DeployConcurrency = n
		}
	}
	if c.ProxyMode() {
		if len(c.AdminTrustedProxies) == 0 || c.AdminUserHeader == "" {
			errs = append(errs, "ADMIN_PASSWORD_HASH is empty (proxy mode): ADMIN_TRUSTED_PROXIES and ADMIN_USER_HEADER are required")
		}
	} else if err := auth.ValidPasswordHash(c.AdminPasswordHash); err != nil {
		errs = append(errs, "ADMIN_PASSWORD_HASH is not a usable argon2id hash ("+err.Error()+"); generate one with `deploy-gateway hash-password`")
	}
	if len(errs) > 0 {
		return Config{}, errors.New(strings.Join(errs, "; "))
	}
	return c, nil
}

// ParsePrefixes parses a comma-separated list of IPs and CIDRs. Prefixes of
// length 0 (0.0.0.0/0, ::/0) are rejected: they would trust every client.
func ParsePrefixes(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "/") {
			p, err := netip.ParsePrefix(part)
			if err != nil {
				return nil, fmt.Errorf("invalid CIDR %q", part)
			}
			if p.Bits() == 0 {
				return nil, fmt.Errorf("%q would trust every client; list the proxy addresses instead", part)
			}
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(part)
		if err != nil {
			return nil, fmt.Errorf("invalid IP %q", part)
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

// parseOwner parses a numeric "uid:gid" with both values in uint32 range.
func parseOwner(s string) (uid, gid int, err error) {
	u, g, ok := strings.Cut(s, ":")
	if !ok {
		return 0, 0, errors.New("missing ':'")
	}
	uv, err := strconv.ParseUint(u, 10, 32)
	if err != nil {
		return 0, 0, err
	}
	gv, err := strconv.ParseUint(g, 10, 32)
	if err != nil {
		return 0, 0, err
	}
	return int(uv), int(gv), nil
}
