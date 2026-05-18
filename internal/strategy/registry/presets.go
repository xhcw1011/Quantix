package registry

import "sync"

// Preset is a named bundle of strategy params, surfaced to the UI as a one-click
// way to start a strategy without manually filling every knob. Each strategy
// package registers its own presets in init().
type Preset struct {
	Name        string         `json:"name"`        // short label, e.g. "Default"
	Description string         `json:"description"` // one-line hint shown under the label
	Params      map[string]any `json:"params"`      // flat key/value passed to strategy.Create
}

var (
	presetMu sync.RWMutex
	presets  = map[string][]Preset{}
)

// RegisterPreset adds a preset for the given strategy name. Strategies may
// register multiple. The order of registration is preserved for display.
func RegisterPreset(strategyName string, p Preset) {
	presetMu.Lock()
	defer presetMu.Unlock()
	presets[strategyName] = append(presets[strategyName], p)
}

// Presets returns all presets registered for a strategy, in registration order.
// Returns nil (not an error) when none are registered.
func Presets(strategyName string) []Preset {
	presetMu.RLock()
	defer presetMu.RUnlock()
	cp := make([]Preset, len(presets[strategyName]))
	copy(cp, presets[strategyName])
	return cp
}
