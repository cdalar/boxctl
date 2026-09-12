// Package client talks to boxctl-vms's REST API
// (/api/vms*), authenticating with a personal token minted from the
// boxctl-web dashboard ("CLI access tokens") rather than the shared
// dashboard token boxctl-web itself uses -- see boxctl-vms's README
// ("Personal API tokens (the `boxctl` CLI)") for how the server scopes a
// personal token's requests to that user's own VMs automatically.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 60 * time.Second},
	}
}

// BaseURL returns the configured API URL (http/https), for cmd/ssh.go to
// derive the ws/wss terminal URL from.
func (c *Client) BaseURL() string { return c.baseURL }

// VM mirrors boxctl-vms's internal/vm.VM as returned by /api/vms* --
// Name/ID always the caller's own bare box name for a personal token
// (the server strips its owner prefix before responding).
type VM struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	IP        string    `json:"ip"`
	State     string    `json:"state"`
	Provider  string    `json:"provider"`
	Ready     bool      `json:"ready"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Image is a boot image offered by boxctl-vms, as returned by
// /api/images -- Name is exactly what a caller passes to Create's
// --image flag. There's no "default" entry: omitting --image entirely
// boots the host's own default rootfs image instead of picking one of
// these by name.
type Image struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Provider is the onctl provider a VM booted from this image will
	// run under ("fc" or "ch"), mirroring VM.Provider.
	Provider string `json:"provider"`
}

// apiError carries the backend's status and body text so callers see
// something more useful than a bare status code.
type apiError struct {
	status int
	body   string
}

func (e *apiError) Error() string {
	body := strings.TrimSpace(e.body)
	if body != "" {
		return fmt.Sprintf("%s (HTTP %d)", body, e.status)
	}
	return fmt.Sprintf("HTTP %d", e.status)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("contacting %s: %w", c.baseURL, err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("unauthorized -- your token may be wrong or revoked; run `boxctl login <token>` again")
	}
	if res.StatusCode >= 300 {
		data, _ := io.ReadAll(res.Body)
		return &apiError{status: res.StatusCode, body: string(data)}
	}
	if out != nil {
		return json.NewDecoder(res.Body).Decode(out)
	}
	return nil
}

func (c *Client) List(ctx context.Context) ([]VM, error) {
	var vms []VM
	if err := c.do(ctx, http.MethodGet, "/api/vms", nil, &vms); err != nil {
		return nil, err
	}
	return vms, nil
}

func (c *Client) ListImages(ctx context.Context) ([]Image, error) {
	var images []Image
	if err := c.do(ctx, http.MethodGet, "/api/images", nil, &images); err != nil {
		return nil, err
	}
	return images, nil
}

// Create sends name as-is (a bare display name); the server prepends
// this token's owner prefix and returns the box with it already
// stripped back off, so the caller never has to think about it.
func (c *Client) Create(ctx context.Context, name, template, image string) (*VM, error) {
	body := map[string]string{"name": name}
	if template != "" {
		body["template"] = template
	}
	if image != "" {
		body["image"] = image
	}
	var vm VM
	if err := c.do(ctx, http.MethodPost, "/api/vms", body, &vm); err != nil {
		return nil, err
	}
	return &vm, nil
}

func (c *Client) Destroy(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/api/vms/"+url.PathEscape(name), nil, nil)
}

func (c *Client) Pause(ctx context.Context, name string) (*VM, error) {
	var vm VM
	if err := c.do(ctx, http.MethodPost, "/api/vms/"+url.PathEscape(name)+"/pause", nil, &vm); err != nil {
		return nil, err
	}
	return &vm, nil
}

func (c *Client) Resume(ctx context.Context, name string) (*VM, error) {
	var vm VM
	if err := c.do(ctx, http.MethodPost, "/api/vms/"+url.PathEscape(name)+"/resume", nil, &vm); err != nil {
		return nil, err
	}
	return &vm, nil
}

// MintTerminalTicket returns a single-use ticket plus the resolved
// (owner-prefixed) vm_id cmd/ssh.go must embed in the /ws/terminal/{id}
// URL -- the CLI has no way to compute that prefix itself.
func (c *Client) MintTerminalTicket(ctx context.Context, name string) (ticket, vmID string, err error) {
	var res struct {
		Ticket string `json:"ticket"`
		VMID   string `json:"vm_id"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/vms/"+url.PathEscape(name)+"/terminal-ticket", nil, &res); err != nil {
		return "", "", err
	}
	return res.Ticket, res.VMID, nil
}
