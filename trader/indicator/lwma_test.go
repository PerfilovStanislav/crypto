package indicator

import (
	"math"
	"testing"
)

// The same values as the analyzer's test in back-rs/src/indicator.rs, so the
// bot sees the signals the backtest saw.
func TestLwma(t *testing.T) {
	got := Lwma.CalculateIndicatorPrices([]float64{1, 2, 3, 4, 5, 6}, 3)
	want := []float64{1, 5.0 / 3, 14.0 / 6, 20.0 / 6, 26.0 / 6, 32.0 / 6}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-12 {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if got := calculateLwma(nil, 3); len(got) != 0 {
		t.Errorf("empty input: %v", got)
	}
}
