package ml

import (
	"math"
	"testing"
)

func TestSigmoid(t *testing.T) {
	tests := []struct {
		z    float64
		want float64
		tol  float64
	}{
		{0, 0.5, 1e-9},
		{1, 0.7310585786, 1e-7},
		{-1, 0.2689414214, 1e-7},
		{100, 1.0, 1e-9},
		{-100, 0.0, 1e-9},
	}
	for _, tc := range tests {
		got := sigmoid(tc.z)
		if math.Abs(got-tc.want) > tc.tol {
			t.Errorf("sigmoid(%v) = %v, want %v (tol %v)", tc.z, got, tc.want, tc.tol)
		}
	}
}

func TestModelPredict(t *testing.T) {
	// A simple model: 2 features, no scaling (mean=0, scale=1)
	m := &Model{
		Coefficients: []float64{1.0, -1.0},
		Intercept:    0.0,
		FeatureNames: []string{"rsi", "bb_pos"},
		Scaler: Scaler{
			Mean:  []float64{0.0, 0.0},
			Scale: []float64{1.0, 1.0},
		},
	}

	// features: rsi=1, bb_pos=-1 → dot = 1*1 + (-1)*(-1) = 2 → sigmoid(2) ≈ 0.8808
	p, err := m.Predict(map[string]float64{"rsi": 1.0, "bb_pos": -1.0})
	if err != nil {
		t.Fatalf("Predict error: %v", err)
	}
	want := sigmoid(2.0)
	if math.Abs(p-want) > 1e-9 {
		t.Errorf("Predict = %v, want %v", p, want)
	}

	// Missing feature should return error
	_, err = m.Predict(map[string]float64{"rsi": 1.0})
	if err == nil {
		t.Error("expected error for missing feature")
	}
}

func TestModelPredictWithScaling(t *testing.T) {
	m := &Model{
		Coefficients: []float64{2.0},
		Intercept:    -1.0,
		FeatureNames: []string{"x"},
		Scaler: Scaler{
			Mean:  []float64{5.0},
			Scale: []float64{2.0},
		},
	}

	// x=7: normalized = (7-5)/2 = 1.0; dot = 2*1 + (-1) = 1; sigmoid(1) ≈ 0.7311
	p, err := m.Predict(map[string]float64{"x": 7.0})
	if err != nil {
		t.Fatalf("Predict error: %v", err)
	}
	want := sigmoid(1.0)
	if math.Abs(p-want) > 1e-9 {
		t.Errorf("Predict = %v, want %v", p, want)
	}
}
