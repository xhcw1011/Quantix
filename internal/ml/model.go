// Package ml provides a lightweight logistic-regression inference engine.
// Model weights are loaded from a JSON file produced by scripts/ml/train.py.
package ml

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
)

// Scaler holds StandardScaler parameters for feature normalisation.
type Scaler struct {
	Mean  []float64 `json:"mean"`
	Scale []float64 `json:"scale"`
}

// Model represents a trained logistic-regression model.
type Model struct {
	Coefficients []float64 `json:"coefficients"`
	Intercept    float64   `json:"intercept"`
	FeatureNames []string  `json:"feature_names"`
	Scaler       Scaler    `json:"scaler"`
}

// LoadModel reads a JSON weight file produced by train.py.
func LoadModel(path string) (*Model, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open model file %q: %w", path, err)
	}
	defer f.Close()

	var m Model
	if err := json.NewDecoder(f).Decode(&m); err != nil {
		return nil, fmt.Errorf("decode model %q: %w", path, err)
	}

	n := len(m.FeatureNames)
	if len(m.Coefficients) != n {
		return nil, fmt.Errorf("model: %d coefficients but %d feature names", len(m.Coefficients), n)
	}
	if len(m.Scaler.Mean) != n || len(m.Scaler.Scale) != n {
		return nil, fmt.Errorf("model: scaler length mismatch (expected %d)", n)
	}

	return &m, nil
}

// Predict returns the probability that the next bar is up (∈ [0, 1]).
// features must contain a value for every name in Model.FeatureNames.
func (m *Model) Predict(features map[string]float64) (float64, error) {
	n := len(m.FeatureNames)
	x := make([]float64, n)
	for i, name := range m.FeatureNames {
		v, ok := features[name]
		if !ok {
			return 0, fmt.Errorf("ml.Predict: missing feature %q", name)
		}
		scale := m.Scaler.Scale[i]
		if scale == 0 {
			scale = 1 // avoid division by zero
		}
		x[i] = (v - m.Scaler.Mean[i]) / scale
	}

	var dot float64
	for i, c := range m.Coefficients {
		dot += c * x[i]
	}
	return sigmoid(dot + m.Intercept), nil
}

func sigmoid(z float64) float64 {
	return 1.0 / (1.0 + math.Exp(-z))
}
