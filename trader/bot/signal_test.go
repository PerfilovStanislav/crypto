package bot

import (
	"math"
	"math/rand"
	"testing"
	"time"

	"trader/config"
	"trader/exchange"
	"trader/indicator"
	"trader/source"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func makeCandles(closes []float64) []exchange.Candle {
	candles := make([]exchange.Candle, len(closes))
	for i, c := range closes {
		candles[i] = exchange.Candle{
			Timestamp: t0.Add(time.Duration(i) * 4 * time.Hour),
			Open:      c, High: c + 1, Low: c - 1, Close: c, Volume: 100,
		}
	}
	return candles
}

func series(n int, f func(i int) float64) []float64 {
	s := make([]float64, n)
	for i := range s {
		s[i] = f(i)
	}
	return s
}

func strategy(ind1, ind2 config.IndicatorConfig) config.StrategyConfig {
	decimals := 2
	return config.StrategyConfig{
		Symbol: "SOLUSDT", Timeframe: "4h", DepositPercent: 5,
		TakeProfit: 2000, StopLoss: 1500, Decimals: &decimals,
		Indicator1: ind1, Indicator2: ind2,
	}
}

var (
	sma5 = config.IndicatorConfig{Type: "sma", Source: "C", Coef: 5}
	sma2 = config.IndicatorConfig{Type: "sma", Source: "C", Coef: 2}
)

// In a steady uptrend sma2 has been above sma5 for many candles: there is no
// crossing on the last candle, but the backtest still counts it as a signal.
func TestSignalIsStateNotCrossing(t *testing.T) {
	s := strategy(sma5, sma2)
	candles := makeCandles(series(200, func(i int) float64 { return 100 + float64(i) }))

	got, err := Signal(candles, s)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatal("want signal while indicator2 > indicator1")
	}

	prev, err := Signal(candles[:len(candles)-1], s)
	if err != nil {
		t.Fatal(err)
	}
	if !prev {
		t.Fatal("setup: the previous candle must also have the signal (no crossing)")
	}
}

func TestSignalFalseWhenBelowOrEqual(t *testing.T) {
	s := strategy(sma5, sma2)

	down := makeCandles(series(200, func(i int) float64 { return 300 - float64(i) }))
	if got, _ := Signal(down, s); got {
		t.Error("downtrend: want no signal")
	}

	flat := makeCandles(series(200, func(int) float64 { return 100 }))
	if got, _ := Signal(flat, s); got {
		t.Error("equal indicators: want no signal, the analyzer uses strict >")
	}
}

// For every closed candle the bot's signal must equal the analyzer condition
// nextPrices[i] > currentPrices[i] computed over the whole history.
func TestSignalMatchesAnalyzerCondition(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	price := 100.0
	closes := series(400, func(int) float64 {
		price += rng.Float64()*4 - 2
		return price
	})
	candles := makeCandles(closes)

	cases := []struct{ ind1, ind2 config.IndicatorConfig }{
		{sma5, config.IndicatorConfig{Type: "ema", Source: "C", Coef: 0.5}},
		{config.IndicatorConfig{Type: "tema", Source: "LOC", Coef: 0.3}, config.IndicatorConfig{Type: "temaZero", Source: "OH", Coef: 0.7}},
		{config.IndicatorConfig{Type: "dema", Source: "L", Coef: 0.4}, config.IndicatorConfig{Type: "sma", Source: "H", Coef: 7}},
	}
	for _, tc := range cases {
		// analyzer: currentPrices = indicator1, nextPrices = indicator2
		quotes := buildQuotes(candles)
		current := indicator.Type(tc.ind1.Type).CalculateIndicatorPrices(getPricesBySource(quotes, source.Type(tc.ind1.Source)), tc.ind1.Coef)
		next := indicator.Type(tc.ind2.Type).CalculateIndicatorPrices(getPricesBySource(quotes, source.Type(tc.ind2.Source)), tc.ind2.Coef)

		s := strategy(tc.ind1, tc.ind2)
		trues, falses := 0, 0
		for n := minSignalCandles; n <= len(candles); n++ {
			got, err := Signal(candles[:n], s)
			if err != nil {
				t.Fatal(err)
			}
			want := next[n-1] > current[n-1]
			if got != want {
				t.Fatalf("%v/%v candle %d: signal %v, analyzer %v", tc.ind1, tc.ind2, n-1, got, want)
			}
			if got {
				trues++
			} else {
				falses++
			}
		}
		if trues == 0 || falses == 0 {
			t.Errorf("%v/%v: weak test data, %d true / %d false", tc.ind1, tc.ind2, trues, falses)
		}
	}
}

func TestSignalErrors(t *testing.T) {
	candles := makeCandles(series(klineLimit-1, func(i int) float64 { return 100 + math.Sin(float64(i)) }))

	// ema coef is a multiplier: 10 diverges to ±Inf on a full kline history
	if _, err := Signal(candles, strategy(sma5, config.IndicatorConfig{Type: "ema", Source: "C", Coef: 10})); err == nil {
		t.Error("diverging ema: want error")
	}
	if _, err := Signal(candles, strategy(sma5, config.IndicatorConfig{Type: "wma", Source: "C", Coef: 3})); err == nil {
		t.Error("unknown type: want error")
	}
	if _, err := Signal(candles, strategy(sma5, config.IndicatorConfig{Type: "sma", Source: "X", Coef: 3})); err == nil {
		t.Error("unknown source: want error")
	}
	if _, err := Signal(candles[:minSignalCandles-1], strategy(sma5, sma2)); err == nil {
		t.Error("short history: want error")
	}
}

func TestClosedCandles(t *testing.T) {
	candles := makeCandles([]float64{1, 2, 3}) // the last one is still forming

	closed, ok := closedCandles(candles, candles[1].Timestamp)
	if !ok || len(closed) != 2 || !closed[1].Timestamp.Equal(candles[1].Timestamp) {
		t.Errorf("closed = %v, ok = %v", closed, ok)
	}

	// Bybit has not rolled over yet: the expected candle is still the newest
	if _, ok := closedCandles(candles, candles[2].Timestamp); ok {
		t.Error("newest candle must not count as closed")
	}
	if _, ok := closedCandles(candles, candles[2].Timestamp.Add(4*time.Hour)); ok {
		t.Error("missing candle must not count as closed")
	}
}

func TestTpSlPrices(t *testing.T) {
	cases := []struct {
		name           string
		entry, tp, sl  float64
		decimals       int
		short          bool
		wantTp, wantSl float64
	}{
		{"SOLUSDT decimals 2", 150, 2000, 1500, 2, false, 170, 135},
		{"decimals 0", 150, 20, 15, 0, false, 170, 135},
		{"decimals 4", 0.5, 200, 100, 4, false, 0.52, 0.49},
		{"short: TP below, SL above", 150, 2000, 1500, 2, true, 130, 165},
	}
	for _, c := range cases {
		tp, sl := TpSlPrices(c.entry, c.tp, c.sl, c.decimals, c.short)
		if math.Abs(tp-c.wantTp) > 1e-9 || math.Abs(sl-c.wantSl) > 1e-9 {
			t.Errorf("%s: got tp %v sl %v, want %v %v", c.name, tp, sl, c.wantTp, c.wantSl)
		}
	}
}

func TestTpSlFormattedWithTick(t *testing.T) {
	inst, err := exchange.NewInstrument("SOL", "USDT", "0.01", "0.0001", "0.0001", "5")
	if err != nil {
		t.Fatal(err)
	}
	tp, sl := TpSlPrices(150.123, 2000, 1500, 2, false)
	if got := inst.FormatPrice(tp); got != "170.12" {
		t.Errorf("tp = %s", got)
	}
	if got := inst.FormatPrice(sl); got != "135.12" {
		t.Errorf("sl = %s", got)
	}
}
