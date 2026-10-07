// Package dockerx wraps the Docker Engine API for the few read and cleanup
// operations the gateway needs. All container creation goes through compose.
package dockerx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"

	"github.com/sebbi08/deployment-gateway/internal/names"
)

type ContainerInfo struct {
	ID     string
	Name   string
	Labels map[string]string
	State  string
	Status string
	Health string
	// Created is when the container was created (zero if unknown).
	Created time.Time
}

type Client struct{ api *client.Client }

func New() (*Client, error) {
	c, err := client.New(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return &Client{api: c}, nil
}

func (c *Client) Ping(ctx context.Context) error {
	_, err := c.api.Ping(ctx, client.PingOptions{})
	return err
}

func (c *Client) ProjectContainers(ctx context.Context, ws, app string) ([]ContainerInfo, error) {
	f := make(client.Filters).Add("label", names.LabelWorkspace+"="+ws).Add("label", names.LabelApp+"="+app)
	return c.list(ctx, f)
}

// WorkspaceContainers lists every container labelled with workspace ws,
// whether or not its app is known to the gateway.
func (c *Client) WorkspaceContainers(ctx context.Context, ws string) ([]ContainerInfo, error) {
	return c.list(ctx, make(client.Filters).Add("label", names.LabelWorkspace+"="+ws))
}

func (c *Client) ManagedContainers(ctx context.Context) ([]ContainerInfo, error) {
	return c.list(ctx, make(client.Filters).Add("label", names.LabelManaged+"=true"))
}

func (c *Client) list(ctx context.Context, f client.Filters) ([]ContainerInfo, error) {
	res, err := c.api.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: f})
	if err != nil {
		return nil, err
	}
	out := make([]ContainerInfo, 0, len(res.Items))
	for _, s := range res.Items {
		info := ContainerInfo{ID: s.ID, Labels: s.Labels, State: string(s.State), Status: s.Status, Created: time.Unix(s.Created, 0)}
		if len(s.Names) > 0 {
			info.Name = strings.TrimPrefix(s.Names[0], "/")
		}
		if s.Health != nil {
			info.Health = string(s.Health.Status)
		} else {
			info.Health = healthFromStatus(s.Status)
		}
		out = append(out, info)
	}
	return out, nil
}

func (c *Client) Inspect(ctx context.Context, id string) (Facts, error) {
	res, err := c.api.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return Facts{}, err
	}
	ir := res.Container
	f := Facts{Name: strings.TrimPrefix(ir.Name, "/")}
	if ir.Config != nil {
		f.Labels = ir.Config.Labels
	}
	if hc := ir.HostConfig; hc != nil {
		f.Privileged = hc.Privileged
		f.CapAdd, f.CapDrop, f.SecurityOpt = hc.CapAdd, hc.CapDrop, hc.SecurityOpt
		f.NetworkMode, f.PidMode, f.IpcMode = string(hc.NetworkMode), string(hc.PidMode), string(hc.IpcMode)
		f.UTSMode, f.UsernsMode, f.Runtime = string(hc.UTSMode), string(hc.UsernsMode), hc.Runtime
		f.Devices, f.Memory = len(hc.Devices), hc.Memory
		if hc.PidsLimit != nil {
			f.PidsLimit = *hc.PidsLimit
		}
		for port, binds := range hc.PortBindings {
			for _, b := range binds {
				hp, _ := strconv.Atoi(b.HostPort)
				ip := ""
				if b.HostIP.IsValid() {
					ip = b.HostIP.String()
				}
				f.PortBindings = append(f.PortBindings, PortFact{ContainerPort: int(port.Num()), Protocol: string(port.Proto()), HostIP: ip, HostPort: hp})
			}
		}
	}
	for _, m := range ir.Mounts {
		f.Mounts = append(f.Mounts, MountFact{Type: string(m.Type), Name: m.Name, Source: m.Source, Destination: m.Destination})
	}
	if ir.State != nil {
		f.State = string(ir.State.Status)
		if ir.State.Health != nil {
			f.Health = string(ir.State.Health.Status)
		}
	}
	return f, nil
}

// ImageLabels returns the LABELs of a locally present image. Docker merges
// them into the labels of every container created from the image.
func (c *Client) ImageLabels(ctx context.Context, ref string) (map[string]string, error) {
	res, err := c.api.ImageInspect(ctx, ref)
	if err != nil {
		return nil, err
	}
	if res.Config == nil {
		return map[string]string{}, nil
	}
	return res.Config.Labels, nil
}

// truncatedMark starts log output whose older part was dropped.
const truncatedMark = "…[earlier output truncated]\n"

// tailWriter keeps the newest max bytes, starting at a line boundary:
// the decisive error is usually at the end of the logs.
type tailWriter struct {
	b         []byte
	max       int
	truncated bool
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.b = append(w.b, p...)
	if len(w.b) > 2*w.max {
		w.trim()
	}
	return len(p), nil
}

func (w *tailWriter) trim() {
	if len(w.b) <= w.max {
		return
	}
	cut := w.b[len(w.b)-w.max:]
	if i := bytes.IndexByte(cut, '\n'); i >= 0 && i < len(cut)-1 {
		cut = cut[i+1:]
	}
	w.b = append([]byte(nil), cut...)
	w.truncated = true
}

func (w *tailWriter) String() string {
	w.trim()
	if w.truncated {
		return truncatedMark + string(w.b)
	}
	return string(w.b)
}

func (c *Client) Logs(ctx context.Context, id string, tail int, since string, maxBytes int) (string, error) {
	rc, err := c.api.ContainerLogs(ctx, id, client.ContainerLogsOptions{
		ShowStdout: true, ShowStderr: true, Tail: strconv.Itoa(tail), Since: since,
	})
	if err != nil {
		return "", err
	}
	defer rc.Close()
	w := &tailWriter{max: maxBytes}
	if _, err := stdcopy.StdCopy(w, w, rc); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return w.String(), nil
}

func (c *Client) NetworkExists(ctx context.Context, name string) (bool, error) {
	_, err := c.api.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
	if err == nil {
		return true, nil
	}
	if cerrdefs.IsNotFound(err) {
		return false, nil
	}
	return false, err
}

func (c *Client) RemoveContainer(ctx context.Context, id string) error {
	_, err := c.api.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	return err
}

// MountSource reports where destination inside containerID comes from on
// the host. inContainer is false when containerID does not exist (the
// gateway is running directly on the host).
func (c *Client) MountSource(ctx context.Context, containerID, destination string) (string, bool, error) {
	res, err := c.api.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	for _, m := range res.Container.Mounts {
		if m.Destination == destination {
			return m.Source, true, nil
		}
	}
	return "", true, nil
}

// RemoveVolume removes a volume; a volume that no longer exists is fine.
func (c *Client) RemoveVolume(ctx context.Context, name string) error {
	_, err := c.api.VolumeRemove(ctx, name, client.VolumeRemoveOptions{})
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	return err
}

// AddressPoolsConfigured reports whether the daemon has custom
// default-address-pools; without them it uses its built-in pools, which
// allow about 31 networks.
func (c *Client) AddressPoolsConfigured(ctx context.Context) (bool, error) {
	res, err := c.api.Info(ctx, client.InfoOptions{})
	if err != nil {
		return false, err
	}
	return len(res.Info.DefaultAddressPools) > 0, nil
}
