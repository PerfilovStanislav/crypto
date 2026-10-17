package bot

import (
	"os"
	"testing"
	"time"

	"trader/config"
	"trader/exchange"
)

// TestLiveSignals prints the current signal of every configured strategy from
// live Bybit candles. Public endpoints only: nothing is bought or cancelled.
//
//	TRADER_LIVE=1 go test ./bot -run TestLiveSignals -v
//	TRADER_LIVE=1 TRADER_CONFIG=/path/config.yaml go test ./bot -run TestLiveSignals -v
func TestLiveSignals(t *testing.T) {
	if os.Getenv("TRADER_LIVE") == "" {
		t.Skip("set TRADER_LIVE=1 to query Bybit")
	}
	path := os.Getenv("TRADER_CONFIG")
	if path == "" {
		path = "../config.yaml"
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	ex := exchange.NewClient(cfg.Bybit)

	for _, s := range cfg.Strategies {
		tf, err := parseTimeframeDuration(s.Timeframe)
		if err != nil {
			t.Fatal(err)
		}
		lastClosed := time.Now().UTC().Truncate(tf).Add(-tf)
		klines, err := ex.GetKlines(s.Symbol, s.Timeframe, klineLimit)
		if err != nil {
			t.Fatalf("%s: %v", s.Symbol, err)
		}
		closed, ok := closedCandles(klines, lastClosed)
		if !ok {
			t.Fatalf("%s: candle %v is not closed on Bybit yet", s.Symbol, lastClosed)
		}
		signal, err := Signal(closed, s)
		if err != nil {
			t.Fatalf("%s: %v", s.Symbol, err)
		}
		last := closed[len(closed)-1]
		t.Logf("%s %s: candle %v close %v, %d closed candles, signal %v",
			s.Symbol, s.Timeframe, last.Timestamp.Format(time.DateTime), last.Close, len(closed), signal)
	}
}
