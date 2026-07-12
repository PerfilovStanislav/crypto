package analyzer

import (
	"config"
	"indicator"
	"math"
	"source"
	"testing"
	"time"
)

func TestAnalyzer_Run_Concurrency(t *testing.T) {
	cfg := config.AnalyzerConfig{
		Threads:    4,
		Pair:       "BTC/USDT",
		Timeframe:  "1h",
		Commission: 0.001,
		MinCloses:  1,
		MinSignals: 1,
		Takeprofit: config.RangeConfig{Start: 10.0, End: 10.0, Step: 1.0},
		Stoploss:   config.RangeConfig{Start: 10.0, End: 10.0, Step: 1.0},
		Indicators: [][]config.IndicatorConfig{
			{
				{
					Type:    indicator.Sma,
					Coefs:   config.RangeConfig{Start: 5.0, End: 5.0, Step: 1.0},
					Sources: []source.Type{source.C},
				},
			},
			{
				{
					Type:    indicator.Sma,
					Coefs:   config.RangeConfig{Start: 10.0, End: 10.0, Step: 1.0},
					Sources: []source.Type{source.C},
				},
			},
		},
	}

	// Create dummy Quotes data
	n := 100
	quotes := source.Quotes{
		Timestamps: make([]time.Time, n),
		Lows:       make([]float64, n),
		Opens:      make([]float64, n),
		Closes:     make([]float64, n),
		Highs:      make([]float64, n),
		Volumes:    make([]float64, n),
	}
	now := time.Now()
	for i := 0; i < n; i++ {
		quotes.Timestamps[i] = now.Add(time.Duration(i) * time.Hour)
		quotes.Opens[i] = 100.0 + float64(i)
		quotes.Highs[i] = quotes.Opens[i] + 15.0
		quotes.Lows[i] = quotes.Opens[i] - 15.0
		quotes.Closes[i] = quotes.Opens[i] + 2.0
		quotes.Volumes[i] = 1000.0
	}

	a := New(cfg, quotes)

	// If the channel bug exists, Run() will panic: send on closed channel
	// We want to verify it doesn't panic.
	a.Run()
}

func TestAnalyzer_testTaskDirect_metrics(t *testing.T) {
	// Let's create an Analyzer instance
	a := &Analyzer{
		Results: make(chan TaskResult, 10),
	}

	// We want to test that testTaskDirect correctly calculates:
	// Coef, MaxDrawdown, ProfitToDd, ProfitToCandles

	// Let's mock the input parameters:
	ic := IndicatorsCompare{}
	tpSlParam := TpSlParam{Tp: 1.0, Sl: 1.0}

	// Signals: we have 2 trades.
	// Trade 1 opens at 1. Close is at 2. Coef is 0.9.
	// Trade 2 opens at 3. Close is at 5. Coef is 1.3.
	// Total coefs length is 10.
	signals := []int{0, 2}

	coefs := []float64{1.0, 0.9, 1.0, 1.3, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0}
	indexes := []int{0, 2, 0, 5, 0, 0, 0, 0, 0, 0}

	a.testTaskDirect(ic, signals, tpSlParam, coefs, indexes)

	// We expect a result since final result is 0.9 * 1.3 = 1.17 > 1.0
	select {
	case res := <-a.Results:
		// Check final Coef
		expectedCoef := 0.9 * 1.3
		if math.Abs(res.Coef-expectedCoef) > 1e-9 {
			t.Errorf("expected Coef %f, got %f", expectedCoef, res.Coef)
		}

		// Peak starts at 1.0.
		// Trade 1: result becomes 0.9. Peak is 1.0. Drawdown is (1.0 - 0.9)/1.0 = 10%.
		// Trade 2: result becomes 0.9 * 1.3 = 1.17. Peak becomes 1.17. Drawdown is (1.17-1.17)/1.17 = 0.
		// So MaxDrawdown is 10%.
		expectedMaxDD := 10.0
		if math.Abs(res.MaxDrawdown-expectedMaxDD) > 1e-9 {
			t.Errorf("expected MaxDrawdown %f%%, got %f%%", expectedMaxDD, res.MaxDrawdown)
		}

		// Profit to Drawdown ratio:
		// profitPct is (1.17 - 1) * 100 = 17%
		// MaxDrawdownPct is 10%
		// ratio is 17 / 10 = 1.7. Or in absolute terms: (1.17 - 1) / 0.1 = 1.7.
		expectedProfitToDd := 1.7
		if math.Abs(res.ProfitToDd-expectedProfitToDd) > 1e-9 {
			t.Errorf("expected ProfitToDd %f, got %f", expectedProfitToDd, res.ProfitToDd)
		}

		// Candles:
		// Trade 1: openingSignalIndex is 0. Opening candle is 1. Closing index is 2.
		// Trade 1 candle count: 2 - 0 = 2.
		// Trade 2: openingSignalIndex is 2. Opening candle is 3. Closing index is 5.
		// Trade 2 candle count: 5 - 2 = 3.
		// Total candles: 2 + 3 = 5.
		// ProfitToCandles: profitPct / 5 = 17 / 5 = 3.4.
		expectedProfitToCandles := 3.4
		if math.Abs(res.ProfitToCandles-expectedProfitToCandles) > 1e-9 {
			t.Errorf("expected ProfitToCandles %f, got %f", expectedProfitToCandles, res.ProfitToCandles)
		}
	default:
		t.Fatal("expected result on channel, but got none")
	}
}
