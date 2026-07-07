package analyzer

import (
	"config"
	"indicator"
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
