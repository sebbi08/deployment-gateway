// Package names defines the naming rules that make every gateway-created
// Docker object name unique and unambiguous (spec §5). Workspace and service
// names contain no hyphens, so <ws>-<app>-<svc> always parses back uniquely.
package names

import (
	"regexp"
	"strings"
)

const (
	LabelWorkspace = "deploy-gateway.workspace"
	LabelApp       = "deploy-gateway.app"
	LabelService   = "deploy-gateway.service"
	LabelManaged   = "deploy-gateway.managed"
	LabelPrefix    = "deploy-gateway."
)

var (
	workspaceRe = regexp.MustCompile(`^[a-z][a-z0-9]{1,15}$`)
	appRe       = regexp.MustCompile(`^[a-z][a-z0-9-]{0,22}[a-z0-9]$`)
	serviceRe   = regexp.MustCompile(`^[a-z][a-z0-9]{0,15}$`)
	templateRe  = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}$`)
)

func ValidWorkspace(s string) bool { return workspaceRe.MatchString(s) }
func ValidApp(s string) bool       { return appRe.MatchString(s) }
func ValidService(s string) bool   { return serviceRe.MatchString(s) }
func ValidTemplate(s string) bool  { return templateRe.MatchString(s) }

// Container is the container name, compose service key and DNS hostname.
func Container(ws, app, svc string) string { return ws + "-" + app + "-" + svc }

// Project is the compose project name.
func Project(ws, app string) string { return "dg-" + ws + "-" + app }

// Network is the per-app private network.
func Network(ws, app string) string { return "dg-" + ws + "-" + app }

// Parse splits a container name back into its parts.
func Parse(name string) (ws, app, svc string, ok bool) {
	first := strings.IndexByte(name, '-')
	last := strings.LastIndexByte(name, '-')
	if first < 0 || last <= first {
		return "", "", "", false
	}
	ws, app, svc = name[:first], name[first+1:last], name[last+1:]
	if !ValidWorkspace(ws) || !ValidApp(app) || !ValidService(svc) {
		return "", "", "", false
	}
	return ws, app, svc, true
}
