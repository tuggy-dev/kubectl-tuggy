package platform

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Registry maps platform names to platforms.
type Registry struct {
	mu        sync.RWMutex
	platforms map[string]Platform
}

// NewRegistry returns an empty registry. Tests use their own registry; the
// CLI uses the package-level one that built-in platforms register into.
func NewRegistry() *Registry {
	return &Registry{platforms: map[string]Platform{}}
}

// Register adds p. It panics if p has no name or the name is taken, because
// that is a programming error found as soon as tuggy starts.
func (r *Registry) Register(p Platform) {
	name := p.Name()
	if name == "" {
		panic("platform: Register called with an empty platform name")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.platforms[name]; dup {
		panic(fmt.Sprintf("platform: %q registered twice", name))
	}
	r.platforms[name] = p
}

// Get returns the platform called name, or an error listing the known ones.
func (r *Registry) Get(name string) (Platform, error) {
	r.mu.RLock()
	p, ok := r.platforms[name]
	r.mu.RUnlock()
	if ok {
		return p, nil
	}

	names := r.Names()
	if len(names) == 0 {
		return nil, fmt.Errorf("unknown platform %q: no platforms are available in this build", name)
	}
	return nil, fmt.Errorf("unknown platform %q (available: %s)", name, strings.Join(names, ", "))
}

// Names returns the registered platform names, sorted.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.platforms))
	for name := range r.platforms {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

var defaultRegistry = NewRegistry()

// Register adds a built-in platform. Platforms call it from init().
func Register(p Platform) { defaultRegistry.Register(p) }

// Get returns a built-in platform by name.
func Get(name string) (Platform, error) { return defaultRegistry.Get(name) }

// Names returns the built-in platform names, sorted.
func Names() []string { return defaultRegistry.Names() }

// Default returns the registry built-in platforms register into.
func Default() *Registry { return defaultRegistry }
