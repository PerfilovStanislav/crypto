package bot

import (
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"trader/config"
	"trader/exchange"
)

func newMartingaleBot(t *testing.T, ex Exchange, path string) *Bot {
	t.Helper()
	b := newTestBot(ex, nil)
	st, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	b.state = st
	return b
}

func martingaleStrategy(maxLosses int) config.StrategyConfig {
	s := strategy(sma5, sma2)
	s.Martingale, s.MaxLosses = 1.2, maxLosses
	return s
}

// tradeAt runs one candle with a signal; every candle gets its own link id.
func tradeAt(t *testing.T, b *Bot, s config.StrategyConfig, candle int) {
	t.Helper()
	b.now = func() time.Time { return testNow.Add(time.Duration(candle) * 4 * time.Hour) }
	if err := b.trade(s, true, 150); err != nil {
		t.Fatal(err)
	}
}

func parse(t *testing.T, v string) float64 {
	t.Helper()
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// closeDeal fires one exit ("sl" or "tp") of the last entry, as Bybit would:
// the coins are sold at its trigger price and the other exit keeps waiting.
func closeDeal(t *testing.T, ex *fakeExchange, exit string) {
	t.Helper()
	n := len(ex.placed)
	fired, waiting := ex.placed[n-2], ex.placed[n-1] // enter places buy, sl, tp
	if exit == "tp" {
		fired, waiting = waiting, fired
	}
	qty := parse(t, fired.Qty)
	ex.balances["SOL"] -= qty
	ex.balances["USDT"] += qty * parse(t, fired.TriggerPrice)
	ex.stopOrders = []exchange.Order{{OrderID: waiting.OrderLinkID, OrderLinkID: waiting.OrderLinkID, Status: "Untriggered"}}
}

func buys(t *testing.T, ex *fakeExchange) []float64 {
	t.Helper()
	var out []float64
	for _, o := range ex.placed {
		if o.Side == "Buy" {
			out = append(out, parse(t, o.Qty))
		}
	}
	return out
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-5 }

func TestMartingaleStakeAfterStopLossAndTakeProfit(t *testing.T) {
	ex := newFakeExchange(t)
	b := newMartingaleBot(t, ex, filepath.Join(t.TempDir(), "state.json"))
	s := martingaleStrategy(5)

	tradeAt(t, b, s, 0) // 5% of 1000 USDT
	closeDeal(t, ex, "sl")
	tradeAt(t, b, s, 1)

	// Bought 0.3327 SOL, SL at 150.123 - 15; TP return from the last close: 20/150.
	loss := 50 - 0.3327*135.123
	st := b.state.get("SOLUSDT")
	if st.Losses != 1 || !near(st.Loss, loss) || st.Base != 50 {
		t.Fatalf("state after SL: %+v, want loss %v", st, loss)
	}
	got := buys(t, ex)
	if want := 50 + 1.2*loss/(20.0/150); len(got) != 2 || !near(got[1], want) {
		t.Fatalf("buys %v, want 50 then %v", got, want)
	}

	closeDeal(t, ex, "tp")
	balance := ex.balances["USDT"]
	tradeAt(t, b, s, 2)
	if st := b.state.get("SOLUSDT"); st.Losses != 0 || st.Loss != 0 {
		t.Fatalf("state after TP: %+v", st)
	}
	if got := buys(t, ex); len(got) != 3 || !near(got[2], balance*0.05) {
		t.Fatalf("buys %v, want the base stake %v after TP", got, balance*0.05)
	}
}

func TestMartingaleStopsAfterMaxLosses(t *testing.T) {
	ex := newFakeExchange(t)
	b := newMartingaleBot(t, ex, filepath.Join(t.TempDir(), "state.json"))
	s := martingaleStrategy(1)

	tradeAt(t, b, s, 0)
	closeDeal(t, ex, "sl")
	tradeAt(t, b, s, 1)
	tradeAt(t, b, s, 2)

	if got := buys(t, ex); len(got) != 1 {
		t.Fatalf("buys %v: a stopped strategy must not enter", got)
	}
	if st := b.state.get("SOLUSDT"); st.Stopped == "" {
		t.Fatalf("state %+v, want stopped", st)
	}
	sent := strings.Join(b.notify.(*fakeNotifier).sent, "\n")
	if !strings.Contains(sent, "стоп-лосс 1 подряд") || !strings.Contains(sent, "остановлена") {
		t.Errorf("notifications %q", sent)
	}
}

func TestMartingaleStopsWhenBalanceIsShort(t *testing.T) {
	ex := newFakeExchange(t)
	b := newMartingaleBot(t, ex, filepath.Join(t.TempDir(), "state.json"))
	s := martingaleStrategy(5)

	tradeAt(t, b, s, 0)
	closeDeal(t, ex, "sl")
	ex.balances["USDT"] = 60 // the next stake is ~95 USDT
	tradeAt(t, b, s, 1)

	if got := buys(t, ex); len(got) != 1 {
		t.Fatalf("buys %v", got)
	}
	if st := b.state.get("SOLUSDT"); !strings.Contains(st.Stopped, "balance") {
		t.Fatalf("state %+v", st)
	}
	if sent := b.notify.(*fakeNotifier).sent; !strings.Contains(sent[len(sent)-1], "на ставку мартингейла") {
		t.Errorf("notifications %q", sent)
	}
}

func TestMartingaleStateSurvivesRestart(t *testing.T) {
	ex := newFakeExchange(t)
	path := filepath.Join(t.TempDir(), "state.json")
	tradeAt(t, newMartingaleBot(t, ex, path), martingaleStrategy(5), 0)

	closeDeal(t, ex, "sl")
	restarted := newMartingaleBot(t, ex, path)
	tradeAt(t, restarted, martingaleStrategy(5), 1)

	if st := restarted.state.get("SOLUSDT"); st.Losses != 1 || st.Open == nil {
		t.Fatalf("state after restart: %+v", st)
	}
	if got := buys(t, ex); len(got) != 2 || got[1] <= 50 {
		t.Fatalf("buys %v, want a bigger stake after the SL", got)
	}
}

func TestMartingaleUnknownExitResetsSeries(t *testing.T) {
	ex := newFakeExchange(t)
	b := newMartingaleBot(t, ex, filepath.Join(t.TempDir(), "state.json"))
	s := martingaleStrategy(5)

	tradeAt(t, b, s, 0)
	closeDeal(t, ex, "sl")
	tradeAt(t, b, s, 1)
	closeDeal(t, ex, "sl")
	ex.stopOrders = nil // both exits gone, e.g. cancelled by hand
	tradeAt(t, b, s, 2)

	if st := b.state.get("SOLUSDT"); st.Losses != 0 || st.Loss != 0 {
		t.Fatalf("state %+v, want a reset series", st)
	}
	if sent := b.notify.(*fakeNotifier).sent; !strings.Contains(strings.Join(sent, "\n"), "серия мартингейла сброшена") {
		t.Errorf("notifications %q", sent)
	}
}
