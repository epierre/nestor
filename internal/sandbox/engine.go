package sandbox

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
)

// Engine is the part of the Docker Engine API sandboxd uses.
type Engine interface {
	Create(ctx context.Context, spec ContainerSpec) (id string, err error)
	Start(ctx context.Context, id string) error
	// Wait blocks until the container stops and returns its exit code. It returns ctx's error
	// when ctx ends first; the container keeps running until removed.
	Wait(ctx context.Context, id string) (exitCode int, err error)
	// Logs returns up to max bytes of the container's stdout and stderr, and whether there was
	// more.
	Logs(ctx context.Context, id string, max int) (out []byte, truncated bool, err error)
	// Remove kills the container if it runs and deletes it with its anonymous volumes. A container
	// that is already gone is not an error.
	Remove(ctx context.Context, id string) error
	// MountSource returns the host path mounted at destination in container.
	MountSource(ctx context.Context, container, destination string) (string, error)
	ImageExists(ctx context.Context, image string) (bool, error)
}

// ContainerSpec is the body of POST /containers/create, limited to the fields sandboxd sets.
type ContainerSpec struct {
	Image           string            `json:"Image"`
	Cmd             []string          `json:"Cmd"`
	Env             []string          `json:"Env"`
	User            string            `json:"User"`
	WorkingDir      string            `json:"WorkingDir"`
	NetworkDisabled bool              `json:"NetworkDisabled"`
	Labels          map[string]string `json:"Labels"`
	HostConfig      HostConfig        `json:"HostConfig"`
}

// HostConfig holds the isolation and resource settings of a run.
type HostConfig struct {
	NetworkMode    string            `json:"NetworkMode"`
	ReadonlyRootfs bool              `json:"ReadonlyRootfs"`
	Binds          []string          `json:"Binds"`
	Tmpfs          map[string]string `json:"Tmpfs"`
	CapDrop        []string          `json:"CapDrop"`
	SecurityOpt    []string          `json:"SecurityOpt"`
	Memory         int64             `json:"Memory"`
	MemorySwap     int64             `json:"MemorySwap"`
	NanoCPUs       int64             `json:"NanoCpus"`
	PidsLimit      int64             `json:"PidsLimit"`
}

// Docker talks to the Docker Engine API over a Unix socket.
type Docker struct {
	client *http.Client
}

// NewDocker returns an Engine using the daemon listening on socket, such as
// /var/run/docker.sock.
func NewDocker(socket string) *Docker {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
	}
	return &Docker{client: &http.Client{Transport: transport}}
}

// apiError is the error body the Engine API returns.
type apiError struct {
	Message string `json:"message"`
}

func (d *Docker) do(ctx context.Context, method, path string, body any, out any, ok ...int) (int, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, reader)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	for _, code := range ok {
		if resp.StatusCode == code {
			if out != nil {
				if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
					return resp.StatusCode, fmt.Errorf("decode %s %s: %w", method, path, err)
				}
			}
			return resp.StatusCode, nil
		}
	}
	var e apiError
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e)
	return resp.StatusCode, fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, e.Message)
}

func (d *Docker) Create(ctx context.Context, spec ContainerSpec) (string, error) {
	var out struct {
		ID string `json:"Id"`
	}
	if _, err := d.do(ctx, http.MethodPost, "/containers/create", spec, &out, http.StatusCreated); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", errors.New("create container: no ID returned")
	}
	return out.ID, nil
}

func (d *Docker) Start(ctx context.Context, id string) error {
	_, err := d.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/start", nil, nil, http.StatusNoContent, http.StatusNotModified)
	return err
}

func (d *Docker) Wait(ctx context.Context, id string) (int, error) {
	var out struct {
		StatusCode int `json:"StatusCode"`
		Error      *struct {
			Message string `json:"Message"`
		} `json:"Error"`
	}
	_, err := d.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/wait?condition=not-running", nil, &out, http.StatusOK)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, err
	}
	if out.Error != nil && out.Error.Message != "" {
		return 0, fmt.Errorf("wait: %s", out.Error.Message)
	}
	return out.StatusCode, nil
}

// Logs reads the multiplexed log stream of a container without a TTY: frames of an 8-byte header
// (stream type, three zero bytes, big-endian payload size) followed by the payload.
func (d *Docker) Logs(ctx context.Context, id string, max int) ([]byte, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://docker/containers/"+url.PathEscape(id)+"/logs?stdout=1&stderr=1", nil)
	if err != nil {
		return nil, false, err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e apiError
		_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e)
		return nil, false, fmt.Errorf("logs: %d %s", resp.StatusCode, e.Message)
	}
	return demux(resp.Body, max)
}

func demux(r io.Reader, max int) ([]byte, bool, error) {
	var out bytes.Buffer
	var header [8]byte
	for {
		if _, err := io.ReadFull(r, header[:]); err != nil {
			if errors.Is(err, io.EOF) {
				return out.Bytes(), false, nil
			}
			return nil, false, fmt.Errorf("read log frame: %w", err)
		}
		size := int64(binary.BigEndian.Uint32(header[4:]))
		room := int64(max - out.Len())
		if size > room {
			if _, err := io.CopyN(&out, r, room); err != nil {
				return nil, false, fmt.Errorf("read log frame: %w", err)
			}
			return out.Bytes(), true, nil
		}
		if _, err := io.CopyN(&out, r, size); err != nil {
			return nil, false, fmt.Errorf("read log frame: %w", err)
		}
	}
}

func (d *Docker) Remove(ctx context.Context, id string) error {
	_, err := d.do(ctx, http.MethodDelete, "/containers/"+url.PathEscape(id)+"?force=1&v=1", nil, nil,
		http.StatusNoContent, http.StatusNotFound)
	return err
}

func (d *Docker) MountSource(ctx context.Context, container, destination string) (string, error) {
	var out struct {
		Mounts []struct {
			Source      string `json:"Source"`
			Destination string `json:"Destination"`
		} `json:"Mounts"`
	}
	if _, err := d.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(container)+"/json", nil, &out, http.StatusOK); err != nil {
		return "", err
	}
	for _, m := range out.Mounts {
		if m.Destination == destination {
			return m.Source, nil
		}
	}
	return "", fmt.Errorf("container %s has no mount at %s", container, destination)
}

func (d *Docker) ImageExists(ctx context.Context, image string) (bool, error) {
	code, err := d.do(ctx, http.MethodGet, "/images/"+url.PathEscape(image)+"/json", nil, nil, http.StatusOK, http.StatusNotFound)
	if err != nil {
		return false, err
	}
	return code == http.StatusOK, nil
}
