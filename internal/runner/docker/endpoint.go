package docker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// Where an Endpoint came from.
const (
	SourceDockerHost    = "DOCKER_HOST"
	SourceDockerContext = "DOCKER_CONTEXT"
	SourceCurrentCtx    = "docker context"
	SourceDefault       = "default"
)

// Endpoint is the container engine tuggy talks to.
type Endpoint struct {
	// Host is the engine address, for example
	// "unix:///Users/me/.colima/default/docker.sock".
	Host string

	// Context is the Docker context name, if one was used.
	Context string

	// Source says how the endpoint was chosen; one of the Source constants.
	Source string
}

func (e Endpoint) String() string {
	if e.Context != "" {
		return fmt.Sprintf("%s (context %q)", e.Host, e.Context)
	}
	return e.Host
}

// DefaultHost is the engine address used when nothing else is configured.
func DefaultHost() string {
	if runtime.GOOS == "windows" {
		return "npipe:////./pipe/docker_engine"
	}
	return "unix:///var/run/docker.sock"
}

// ResolveEndpoint finds the container engine the same way the docker CLI
// does, so tuggy uses whatever engine "docker ps" uses:
//
//  1. DOCKER_HOST
//  2. DOCKER_CONTEXT
//  3. the current context in the Docker config file ("docker context use")
//  4. the operating system's default socket or pipe
//
// getenv reads environment variables; configDir is the Docker config
// directory ($DOCKER_CONFIG or ~/.docker).
func ResolveEndpoint(getenv func(string) string, configDir string) (Endpoint, error) {
	if host := getenv("DOCKER_HOST"); host != "" {
		return Endpoint{Host: host, Source: SourceDockerHost}, nil
	}

	name, source := getenv("DOCKER_CONTEXT"), SourceDockerContext
	if name == "" {
		current, err := currentContext(configDir)
		if err != nil {
			return Endpoint{}, err
		}
		name, source = current, SourceCurrentCtx
	}
	if name == "" || name == "default" {
		return Endpoint{Host: DefaultHost(), Source: SourceDefault}, nil
	}

	host, err := contextHost(configDir, name)
	if err != nil {
		return Endpoint{}, err
	}
	return Endpoint{Host: host, Context: name, Source: source}, nil
}

// DefaultConfigDir returns $DOCKER_CONFIG or ~/.docker.
func DefaultConfigDir(getenv func(string) string) string {
	if dir := getenv("DOCKER_CONFIG"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".docker")
}

func currentContext(configDir string) (string, error) {
	if configDir == "" {
		return "", nil
	}
	data, err := os.ReadFile(filepath.Join(configDir, "config.json")) // #nosec G304 -- the user's Docker config
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading Docker config: %w", err)
	}
	var cfg struct {
		CurrentContext string `json:"currentContext"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", fmt.Errorf("reading Docker config %s: %w", filepath.Join(configDir, "config.json"), err)
	}
	return cfg.CurrentContext, nil
}

// contextHost reads a context's engine address. The docker CLI stores each
// context under contexts/meta/<sha256 of the name>/meta.json.
func contextHost(configDir, name string) (string, error) {
	sum := sha256.Sum256([]byte(name))
	path := filepath.Join(configDir, "contexts", "meta", hex.EncodeToString(sum[:]), "meta.json")

	data, err := os.ReadFile(path) // #nosec G304 -- path is derived from the Docker config directory
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("docker context %q not found (see `docker context ls`)", name)
	}
	if err != nil {
		return "", fmt.Errorf("reading docker context %q: %w", name, err)
	}

	var meta struct {
		Endpoints map[string]struct {
			Host string `json:"Host"`
		} `json:"Endpoints"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return "", fmt.Errorf("reading docker context %q: %w", name, err)
	}
	host := meta.Endpoints["docker"].Host
	if host == "" {
		return "", fmt.Errorf("docker context %q has no Docker endpoint", name)
	}
	return host, nil
}
