package gke

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/oauth2/google"

	"github.com/tuggy-dev/kubectl-tuggy/internal/runner"
)

func TestADCPath(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		goos string
		want string
	}{
		{"explicit file wins", map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": "/keys/sa.json", "CLOUDSDK_CONFIG": "/cfg"}, "linux", "/keys/sa.json"},
		{"gcloud config dir", map[string]string{"CLOUDSDK_CONFIG": "/cfg"}, "linux", filepath.Join("/cfg", "application_default_credentials.json")},
		{"linux and mac default", nil, "darwin", filepath.Join("/home/me", ".config", "gcloud", "application_default_credentials.json")},
		{"windows default", map[string]string{"APPDATA": `C:\Users\me\AppData\Roaming`}, "windows", filepath.Join(`C:\Users\me\AppData\Roaming`, "gcloud", "application_default_credentials.json")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := adcPath(func(k string) string { return tt.env[k] }, tt.goos, "/home/me")
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func writeCredentials(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "application_default_credentials.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const userCredentials = `{"type":"authorized_user","client_id":"id","client_secret":"secret","refresh_token":"refresh"}`

func TestLoadCredentials(t *testing.T) {
	tests := []struct {
		name, content string
		wantType      google.CredentialsType
		wantAccount   string
	}{
		{"gcloud login", userCredentials, google.AuthorizedUser, ""},
		{"service account key", `{"type":"service_account","client_email":"ci@acme-dev.iam.gserviceaccount.com"}`, google.ServiceAccount, "ci@acme-dev.iam.gserviceaccount.com"},
		{"impersonated", `{"type":"impersonated_service_account","service_account_impersonation_url":"https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/opentofu@acme-dev.iam.gserviceaccount.com:generateAccessToken","source_credentials":{}}`, google.ImpersonatedServiceAccount, "opentofu@acme-dev.iam.gserviceaccount.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := loadCredentials(writeCredentials(t, tt.content))
			if err != nil {
				t.Fatal(err)
			}
			if c.Type != tt.wantType || c.Account != tt.wantAccount || !filepath.IsAbs(c.Path) {
				t.Errorf("got %+v", c)
			}
		})
	}
}

func TestLoadCredentialsErrors(t *testing.T) {
	if _, err := loadCredentials(filepath.Join(t.TempDir(), "missing.json")); !errors.Is(err, errNoCredentials) {
		t.Errorf("missing file: %v, want errNoCredentials", err)
	}
	if _, err := loadCredentials(writeCredentials(t, "not json")); err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Errorf("bad JSON: %v", err)
	}
	if _, err := loadCredentials(writeCredentials(t, `{"type":"gdch_service_account"}`)); err == nil || !strings.Contains(err.Error(), "unsupported type") {
		t.Errorf("unsupported type: %v", err)
	}
}

func TestCredentialsInContainer(t *testing.T) {
	c, err := loadCredentials(writeCredentials(t, userCredentials))
	if err != nil {
		t.Fatal(err)
	}
	mounts, env := c.container()
	want := runner.Mount{Type: runner.MountBind, Source: c.Path, Target: containerCredentialsPath, ReadOnly: true}
	if len(mounts) != 1 || mounts[0] != want {
		t.Errorf("mounts = %+v, want only the credentials file, read-only", mounts)
	}
	if env["GOOGLE_APPLICATION_CREDENTIALS"] != containerCredentialsPath {
		t.Errorf("env = %v", env)
	}
}
