// Package registry provides a global strategy factory registry.
// Each strategy package registers itself via its init() function.
package registry

import (
	"fmt"
	"sort"
	"sync"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/strategy"
)

// FactoryFn creates a Strategy from a params map.
type FactoryFn func(params map[string]any, log *zap.Logger) (strategy.Strategy, error)

var (
	mu       sync.RWMutex
	registry = map[string]FactoryFn{}
)

// Register adds a factory function under the given name.
// It panics if the same name is registered twice.
func Register(name string, fn FactoryFn) {
	mu.Lock()
	defer mu.Unlock()
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("strategy registry: duplicate registration for %q", name))
	}
	registry[name] = fn
}

// Create instantiates a strategy by name.
func Create(name string, params map[string]any, log *zap.Logger) (strategy.Strategy, error) {
	mu.RLock()
	fn, ok := registry[name]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("strategy %q not registered; available: %v", name, Names())
	}
	return fn(params, log)
}

// Exists reports whether a strategy is registered under the given name.
func Exists(name string) bool {
	mu.RLock()
	_, ok := registry[name]
	mu.RUnlock()
	return ok
}

// Names returns all registered strategy names in sorted order.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
