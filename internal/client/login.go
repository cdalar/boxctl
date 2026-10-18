package client

import (
	"context"
	"errors"
	"net/http"
	"net/url"
)

// LoginStart is boxctl-vms's answer to POST /api/cli-login: a pending
// browser login (docs/plans/browser-login.md). ID is the CLI's secret
// poll key; UserCode is what the approval page shows the human;
// VerifyURL is that page.
type LoginStart struct {
	ID        string `json:"id"`
	UserCode  string `json:"user_code"`
	VerifyURL string `json:"verify_url"`
	ExpiresIn int    `json:"expires_in"` // seconds
	Interval  int    `json:"interval"`   // seconds between polls
}

// LoginStatus is GET /api/cli-login/{id}: Status is "pending",
// "approved", "denied" or "expired", and Token is set exactly once, on
// "approved". Email is who approved it, when the server knows.
type LoginStatus struct {
	Status string `json:"status"`
	Token  string `json:"token,omitempty"`
	Email  string `json:"email,omitempty"`
}

// ErrNoBrowserLogin means the server has no /api/cli-login: an older
// boxctl-vms, where only a pasted token works.
var ErrNoBrowserLogin = errors.New("this server has no browser login")

// StartLogin begins a browser login. redirectURI and state are the
// CLI's loopback callback (http://127.0.0.1:<port>/callback) and its
// nonce, both empty for a headless login that polls instead. Needs no
// token: the client is built with an empty one.
func (c *Client) StartLogin(ctx context.Context, label, redirectURI, state string) (*LoginStart, error) {
	body := map[string]string{"label": label}
	if redirectURI != "" {
		body["redirect_uri"] = redirectURI
		body["state"] = state
	}
	var out LoginStart
	if err := c.do(ctx, http.MethodPost, "/api/cli-login", body, &out); err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.status == http.StatusNotFound {
			return nil, ErrNoBrowserLogin
		}
		return nil, err
	}
	if out.ID == "" || out.VerifyURL == "" {
		return nil, errors.New("the server's login reply had no id or verify_url")
	}
	return &out, nil
}

// PollLogin reports a pending login's state; on "approved" the token is
// in the result and the server forgets the login.
func (c *Client) PollLogin(ctx context.Context, id string) (*LoginStatus, error) {
	var out LoginStatus
	if err := c.do(ctx, http.MethodGet, "/api/cli-login/"+url.PathEscape(id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
