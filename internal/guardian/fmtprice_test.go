package guardian

import "testing"

func TestFmtPrice(t *testing.T) {
	cases := map[float64]string{
		64127.7:  "64,127.70",
		67334.09: "67,334.09",
		1234567:  "1,234,567.00",
		999:      "999.00",
		0.5:      "0.500000",
		0:        "0.00",
	}
	for in, want := range cases {
		if got := fmtPrice(in); got != want {
			t.Errorf("fmtPrice(%v) = %q, want %q", in, got, want)
		}
	}
}
