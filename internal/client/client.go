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
	"errors"
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
	return c.doWith(c.http, ctx, method, path, body, out)
}

// doLong is do's sibling for a call whose duration is bounded by the
// caller's own timeout/ctx rather than c.http's fixed 60s -- Exec's
// server-side wait can legitimately run for minutes. Reuses streamClient
// (no fixed Timeout) for exactly the same reason Download/Import's byte
// transfers do (see streamClient's doc comment).
func (c *Client) doLong(ctx context.Context, method, path string, body, out any) error {
	return c.doWith(streamClient, ctx, method, path, body, out)
}

func (c *Client) doWith(hc *http.Client, ctx context.Context, method, path string, body, out any) error {
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

	res, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("contacting %s: %w", c.baseURL, err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("unauthorized -- your token may be wrong or revoked; run `boxctl login` again")
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

// streamClient is used for the actual bundle-byte transfers in
// Download/Import -- both now go straight to R2 (see boxctl-vms's
// docs/plans/s3-transfer.md), not through boxctl-vms itself -- and,
// via doLong, for Exec's own JSON call to boxctl-vms, which blocks
// server-side for as long as the remote command takes to run. All three
// need a client with no fixed timeout (unlike c.http's 60s, fine for
// every other, fast JSON call this client makes) since their duration
// is bounded by something else instead (size/bandwidth for a transfer,
// the command's own timeout for Exec). Relies on the caller's context
// for cancellation.
var streamClient = &http.Client{}

// waitReadyPollInterval paces WaitReady's polling of GET /api/vms while a
// freshly created box finishes booting.
const waitReadyPollInterval = 500 * time.Millisecond

// WaitReady polls until name reports ready (see VM.Ready's doc comment)
// or timeout elapses -- boxctl.io's own dashboard says a box is "usually
// ready in a few seconds", but there's no per-VM status endpoint to poll
// instead of the full list (see List), so this just filters List's
// result by name each tick. Used by Exec's ephemeral box, where a
// created-but-not-yet-reachable box would otherwise just fail the exec
// call outright.
func (c *Client) WaitReady(ctx context.Context, name string, timeout time.Duration) (*VM, error) {
	deadline := time.Now().Add(timeout)
	start := time.Now()
	for tick := 0; ; tick++ {
		vms, err := c.List(ctx)
		if err != nil {
			finishWaiting()
			return nil, err
		}
		for i := range vms {
			if vms[i].Name == name && vms[i].Ready {
				finishWaiting()
				return &vms[i], nil
			}
		}
		if time.Now().After(deadline) {
			finishWaiting()
			return nil, fmt.Errorf("timed out after %s waiting for %s to become ready", timeout, name)
		}
		printWaiting(fmt.Sprintf("waiting for %s to boot", name), tick, time.Since(start))
		select {
		case <-ctx.Done():
			finishWaiting()
			return nil, ctx.Err()
		case <-time.After(waitReadyPollInterval):
		}
	}
}

// ExecResult is POST /api/vms/{name}/exec's response.
type ExecResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	// Error is set only for a transport-level failure (couldn't reach
	// the box at all, or the command timed out) -- a normal nonzero
	// exit from the command itself is reported via ExitCode alone, not
	// this field.
	Error string `json:"error,omitempty"`
}

// Exec runs command on name over SSH, no pty -- unlike the interactive
// ssh session above (WebSocket+pty relay), stdout/stderr/exit code come
// back cleanly separated, for a caller (a script, an AI agent) that
// needs a structured result rather than a terminal transcript. Blocks
// server-side until the command finishes or timeout elapses --
// boxctl-vms itself enforces timeout host-side too; this call's own HTTP
// deadline is timeout plus headroom for the round trip, via doLong since
// c.http's fixed 60s would otherwise cut off a longer-running command.
func (c *Client) Exec(ctx context.Context, name, command string, timeout time.Duration) (*ExecResult, error) {
	body := map[string]any{
		"command":         command,
		"timeout_seconds": int(timeout.Seconds()),
	}
	ctx, cancel := context.WithTimeout(ctx, timeout+15*time.Second)
	defer cancel()

	var result ExecResult
	if err := c.doLong(ctx, http.MethodPost, "/api/vms/"+url.PathEscape(name)+"/exec", body, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// transferPollInterval paces Download/Import's polling of boxctl-vms's
// export/import status endpoints while the agent works in the
// background (building+uploading a bundle, or fetching+reconstructing
// one) -- see pollTransfer.
const transferPollInterval = 1 * time.Second

// transferStatus is the shape both GET /api/vms/{id}/export/{export_id}
// and GET /api/imports/{import_id} respond with -- see boxctl-vms's
// docs/plans/s3-transfer.md. Only the field relevant to whichever one is
// actually populated.
type transferStatus struct {
	Status      string `json:"status"`
	DownloadURL string `json:"download_url,omitempty"`
	VM          *VM    `json:"vm,omitempty"`
	Error       string `json:"error,omitempty"`
}

// Download streams name's paused snapshot bundle to w. As of boxctl-vms's
// docs/plans/s3-transfer.md this is a three-step dance hidden entirely
// behind this one call: kick off an export (POST .../export), poll until
// the agent's finished building and uploading the bundle to R2 (GET
// .../export/{export_id}), then stream straight from the presigned R2
// URL that returns -- boxctl-vms is never in the byte path itself. See
// vmbundle.Stream for exactly what's in the bundle
// (docs/plans/vm-snapshot-download.md).
func (c *Client) Download(ctx context.Context, name string, w io.Writer) error {
	var kickoff struct {
		ExportID string `json:"export_id"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/vms/"+url.PathEscape(name)+"/export", nil, &kickoff); err != nil {
		return err
	}

	status, err := c.pollTransfer(ctx, "/api/vms/"+url.PathEscape(name)+"/export/"+url.PathEscape(kickoff.ExportID), "waiting for the host to build and upload the bundle")
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, status.DownloadURL, nil)
	if err != nil {
		return err
	}
	res, err := streamClient.Do(req)
	if err != nil {
		return fmt.Errorf("downloading bundle from object storage: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode >= 300 {
		data, _ := io.ReadAll(res.Body)
		return fmt.Errorf("downloading bundle from object storage: http %d: %s", res.StatusCode, string(data))
	}

	// total is 0 (unknown) unless R2 reported a real Content-Length --
	// only true for a "diff" export (see boxctl-vms's
	// vmbundle.SizedBundle); a "full" export is unsized upfront, so
	// progress there is just a running byte count instead of a
	// percentage.
	pw := &progressWriter{w: w}
	if res.ContentLength > 0 {
		pw.total = res.ContentLength
	}
	_, err = io.Copy(pw, res.Body)
	finishProgress(pw.written, pw.total)
	return err
}

// Import uploads r (size bytes, a bundle previously produced by
// Download) as a new box named name. As of boxctl-vms's
// docs/plans/s3-transfer.md this is a four-step dance hidden entirely
// behind this one call: kick off an import (POST /api/vms/import,
// returning a presigned R2 upload URL), PUT r straight to R2 (boxctl-vms
// is never in the byte path itself), tell boxctl-vms the upload finished
// (POST .../complete), then poll until the agent's fetched the bundle
// back from R2 and reconstructed it (GET /api/imports/{import_id}).
// size doubles as both the upload's Content-Length and the progress
// display's total.
func (c *Client) Import(ctx context.Context, name string, r io.Reader, size int64) (*VM, error) {
	var kickoff struct {
		ImportID  string `json:"import_id"`
		UploadURL string `json:"upload_url"`
		Name      string `json:"name"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/vms/import", map[string]string{"name": name}, &kickoff); err != nil {
		return nil, err
	}

	pr := &progressReader{r: r, total: size}
	putReq, err := http.NewRequestWithContext(ctx, http.MethodPut, kickoff.UploadURL, pr)
	if err != nil {
		return nil, err
	}
	if size > 0 {
		putReq.ContentLength = size
	}
	putRes, err := streamClient.Do(putReq)
	finishProgress(pr.read, pr.total)
	if err != nil {
		return nil, fmt.Errorf("uploading bundle to object storage: %w", err)
	}
	defer func() { _ = putRes.Body.Close() }()
	if putRes.StatusCode >= 300 {
		data, _ := io.ReadAll(putRes.Body)
		return nil, fmt.Errorf("uploading bundle to object storage: http %d: %s", putRes.StatusCode, string(data))
	}

	completePath := "/api/imports/" + url.PathEscape(kickoff.ImportID) + "/complete?name=" + url.QueryEscape(kickoff.Name)
	if err := c.do(ctx, http.MethodPost, completePath, nil, nil); err != nil {
		return nil, err
	}

	status, err := c.pollTransfer(ctx, "/api/imports/"+url.PathEscape(kickoff.ImportID), "waiting for the host to reconstruct the box")
	if err != nil {
		return nil, err
	}
	return status.VM, nil
}

// pollTransfer polls path (an export or import status endpoint) at
// transferPollInterval until it reports "done" or "failed", or ctx ends.
// label names what it's waiting on (e.g. "waiting for aimax to build and
// upload the bundle") for printWaiting's spinner -- there's nothing else
// to show while this runs (no byte count, no percentage: the backend
// doesn't report incremental progress for either its build+upload or
// fetch+reconstruct step), so without this a caller sees total silence
// for anywhere from seconds to a couple of minutes and no way to tell
// "still working" from "stuck".
func (c *Client) pollTransfer(ctx context.Context, path, label string) (*transferStatus, error) {
	start := time.Now()
	for tick := 0; ; tick++ {
		var status transferStatus
		if err := c.do(ctx, http.MethodGet, path, nil, &status); err != nil {
			finishWaiting()
			return nil, err
		}
		switch status.Status {
		case "done":
			finishWaiting()
			return &status, nil
		case "failed":
			finishWaiting()
			return nil, errors.New(status.Error)
		}
		printWaiting(label, tick, time.Since(start))
		select {
		case <-ctx.Done():
			finishWaiting()
			return nil, ctx.Err()
		case <-time.After(transferPollInterval):
		}
	}
}
