package gke

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"

	"golang.org/x/oauth2/google"

	"github.com/tuggy-dev/kubectl-tuggy/internal/runner"
)

// Where the credentials file appears inside the OpenTofu container.
const containerCredentialsPath = "/tuggy/gcloud/application_default_credentials.json" // #nosec G101 -- a path, not a credential

// errNoCredentials means no Application Default Credentials were found.
var errNoCredentials = errors.New("no Google credentials found")

// Credentials are the user's Google Application Default Credentials (ADC):
// the file created by "gcloud auth application-default login", or a service
// account key named by GOOGLE_APPLICATION_CREDENTIALS.
type Credentials struct {
	// Path is the credentials file on this machine.
	Path string

	// Type is the file's credential type, for example "authorized_user".
	Type google.CredentialsType

	// Account is the identity, when the file names one: a service account's
	// email, or the service account an impersonated credential acts as.
	Account string

	data []byte
}

// supportedTypes are the credential types tuggy accepts. They are checked
// before the file is used, as Google's library requires for files from
// outside the program.
var supportedTypes = map[google.CredentialsType]bool{
	google.AuthorizedUser:             true,
	google.ServiceAccount:             true,
	google.ImpersonatedServiceAccount: true,
	google.ExternalAccount:            true,
}

// adcPath returns where the ADC file should be, in the order Google's own
// tools use: GOOGLE_APPLICATION_CREDENTIALS, then the gcloud configuration
// directory ($CLOUDSDK_CONFIG, %APPDATA%\gcloud on Windows, or
// ~/.config/gcloud).
func adcPath(getenv func(string) string, goos, home string) string {
	if p := getenv("GOOGLE_APPLICATION_CREDENTIALS"); p != "" {
		return p
	}
	dir := getenv("CLOUDSDK_CONFIG")
	if dir == "" {
		if goos == "windows" {
			dir = filepath.Join(getenv("APPDATA"), "gcloud")
		} else {
			dir = filepath.Join(home, ".config", "gcloud")
		}
	}
	return filepath.Join(dir, "application_default_credentials.json")
}

var impersonationURL = regexp.MustCompile(`serviceAccounts/([^/:]+):generateAccessToken`)

// loadCredentials reads and checks a credentials file.
func loadCredentials(path string) (*Credentials, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- the user's own credentials file
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w at %s", errNoCredentials, path)
	}
	if err != nil {
		return nil, fmt.Errorf("reading Google credentials %s: %w", path, err)
	}

	var meta struct {
		Type                           string `json:"type"`
		ClientEmail                    string `json:"client_email"`
		ServiceAccountImpersonationURL string `json:"service_account_impersonation_url"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("credentials file %s is not valid JSON: %w", path, err)
	}
	credType := google.CredentialsType(meta.Type)
	if !supportedTypes[credType] {
		return nil, fmt.Errorf("credentials file %s has unsupported type %q", path, meta.Type)
	}

	account := meta.ClientEmail
	if m := impersonationURL.FindStringSubmatch(meta.ServiceAccountImpersonationURL); m != nil {
		account = m[1]
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	return &Credentials{Path: abs, Type: credType, Account: account, data: data}, nil
}

// container returns the mount and environment that make these credentials
// available to OpenTofu inside the container. Only the single file is
// mounted, read-only.
func (c *Credentials) container() ([]runner.Mount, map[string]string) {
	return []runner.Mount{{Type: runner.MountBind, Source: c.Path, Target: containerCredentialsPath, ReadOnly: true}},
		map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": containerCredentialsPath}
}

// describe says which credentials were found, for doctor.
func (c *Credentials) describe() string {
	s := fmt.Sprintf("%s (%s)", c.Path, c.Type)
	if c.Account != "" {
		s += ", account " + c.Account
	}
	return s
}
