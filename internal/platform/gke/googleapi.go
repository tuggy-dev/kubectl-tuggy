package gke

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

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

// googleAPI is the handful of Google Cloud REST calls preflight needs. Base
// URLs are fields so tests can point them at a local server.
type googleAPI struct {
	ResourceManagerURL string
	ServiceUsageURL    string
	IAMCredentialsURL  string
	HTTPClient         *http.Client

	// TokenSource turns credentials into access tokens. Tests replace it.
	TokenSource func(ctx context.Context, c *Credentials) (oauth2.TokenSource, error)
}

func newGoogleAPI() *googleAPI {
	return &googleAPI{ // #nosec G101 -- public API endpoints, not credentials
		ResourceManagerURL: "https://cloudresourcemanager.googleapis.com",
		ServiceUsageURL:    "https://serviceusage.googleapis.com",
		IAMCredentialsURL:  "https://iamcredentials.googleapis.com",
		HTTPClient:         &http.Client{Timeout: 30 * time.Second},
		TokenSource:        googleTokenSource,
	}
}

func googleTokenSource(ctx context.Context, c *Credentials) (oauth2.TokenSource, error) {
	creds, err := google.CredentialsFromJSONWithType(ctx, c.data, c.Type, cloudPlatformScope)
	if err != nil {
		return nil, err
	}
	return creds.TokenSource, nil
}

// apiError is a non-2xx response from a Google API.
type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("Google API returned HTTP %d", e.Status)
	}
	return fmt.Sprintf("%s (HTTP %d)", e.Message, e.Status)
}

func (g *googleAPI) call(ctx context.Context, token, method, rawURL string, body, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := g.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		return &apiError{Status: resp.StatusCode, Message: e.Error.Message}
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// grantedPermissions returns which of perms the token holds on project.
func (g *googleAPI) grantedPermissions(ctx context.Context, token, project string, perms []string) ([]string, error) {
	var out struct {
		Permissions []string `json:"permissions"`
	}
	u := fmt.Sprintf("%s/v1/projects/%s:testIamPermissions", g.ResourceManagerURL, url.PathEscape(project))
	err := g.call(ctx, token, http.MethodPost, u, map[string]any{"permissions": perms}, &out)
	return out.Permissions, err
}

// serviceEnabled reports whether an API, such as container.googleapis.com,
// is enabled in project.
func (g *googleAPI) serviceEnabled(ctx context.Context, token, project, service string) (bool, error) {
	var out struct {
		State string `json:"state"`
	}
	u := fmt.Sprintf("%s/v1/projects/%s/services/%s", g.ServiceUsageURL, url.PathEscape(project), url.PathEscape(service))
	if err := g.call(ctx, token, http.MethodGet, u, nil, &out); err != nil {
		return false, err
	}
	return strings.EqualFold(out.State, "ENABLED"), nil
}

// impersonate returns an access token for serviceAccount, obtained with token.
func (g *googleAPI) impersonate(ctx context.Context, token, serviceAccount string) (string, error) {
	var out struct {
		AccessToken string `json:"accessToken"`
	}
	u := fmt.Sprintf("%s/v1/projects/-/serviceAccounts/%s:generateAccessToken", g.IAMCredentialsURL, url.PathEscape(serviceAccount))
	err := g.call(ctx, token, http.MethodPost, u, map[string]any{"scope": []string{cloudPlatformScope}}, &out)
	return out.AccessToken, err
}
