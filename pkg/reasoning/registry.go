package reasoning

import (
	"fmt"
	"sort"
	"sync"
)

// Factory builds a Reasoner from a graph + predicate registry + config. A plugin
// backend registers a Factory under a name at init time; a host selects one by
// name via New, so backends are swappable without a compile-time dependency on a
// concrete implementation.
type Factory func(g GraphQuerier, reg *PredicateRegistry, cfg Config) (Reasoner, error)

var (
	mu        sync.RWMutex
	factories = map[string]Factory{}
)

// Register adds a named reasoner backend. Intended to be called from a backend's
// init(). Panics on duplicate name (a programming error).
func Register(name string, f Factory) {
	if name == "" || f == nil {
		panic("reasoning.Register: empty name or nil factory")
	}
	mu.Lock()
	defer mu.Unlock()
	if _, dup := factories[name]; dup {
		panic("reasoning.Register: duplicate backend " + name)
	}
	factories[name] = f
}

// New constructs the named reasoner backend.
func New(name string, g GraphQuerier, reg *PredicateRegistry, cfg Config) (Reasoner, error) {
	mu.RLock()
	f, ok := factories[name]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("reasoning: no backend %q registered (have: %v)", name, Backends())
	}
	return f(g, reg, cfg)
}

// Backends lists the registered backend names (sorted).
func Backends() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(factories))
	for n := range factories {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// BackendName is the built-in ladybug symbolic backend's registry key.
const BackendName = "symbolic-graph"

func init() {
	Register(BackendName, func(g GraphQuerier, reg *PredicateRegistry, cfg Config) (Reasoner, error) {
		return NewEngine(g, reg, cfg), nil
	})
}
