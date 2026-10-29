package bot

import (
	"math"
	"os"
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
	b := newTestBot(ex)
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

// closeDeal fires the TP or SL ("tp"/"sl") of the entry made on candle, as
// Bybit would: the side is closed at that price, the other exit goes with the
// position, and the fired one shows in the order history an hour later.
func closeDeal(t *testing.T, ex *fakeExchange, exit string, candle int) {
	t.Helper()
	idx := ex.placed[len(ex.placed)-1].PositionIdx
	price, kind := ex.tpsl[idx][0], "TakeProfit"
	if exit == "sl" {
		price, kind = ex.tpsl[idx][1], "StopLoss"
	}
	pnl := ex.positions[idx] * (parse(t, price) - ex.fillPrice)
	if idx == 2 { // a short
		pnl = -pnl
	}
	ex.usdt += pnl
	ex.positions[idx] = 0
	delete(ex.tpsl, idx)
	at := testNow.Add(time.Duration(candle)*4*time.Hour + time.Hour)
	ex.history = append([]exchange.Order{{Status: "Filled", PositionIdx: idx, StopOrderType: kind, UpdatedTime: at}}, ex.history...)
}

// stakes are the USDT values of the entries at the last close 150.
func stakes(t *testing.T, ex *fakeExchange) []float64 {
	t.Helper()
	var out []float64
	for _, o := range ex.placed {
		if !o.ReduceOnly {
			out = append(out, parse(t, o.Qty)*150)
		}
	}
	return out
}

// near compares USDT values up to one qty step (0.0001 SOL at 150).
func near(a, b float64) bool { return math.Abs(a-b) <= 0.0151 }

func TestMartingaleStakeAfterStopLossAndTakeProfit(t *testing.T) {
	ex := newFakeExchange(t)
	b := newMartingaleBot(t, ex, filepath.Join(t.TempDir(), "state.json"))
	s := martingaleStrategy(5)

	tradeAt(t, b, s, 0) // 5% of 1000 USDT
	closeDeal(t, ex, "sl", 0)
	tradeAt(t, b, s, 1)

	// Opened 0.3333 SOL at 150.123, SL 15 lower; TP return from the last close: 20/150.
	loss := 0.3333 * 15.0
	st := b.state.get("SOLUSDT-L")
	if st.Losses != 1 || math.Abs(st.Loss-loss) > 1e-9 || st.Base != 50 {
		t.Fatalf("state after SL: %+v, want loss %v", st, loss)
	}
	got := stakes(t, ex)
	if want := 50 + 1.2*loss/(20.0/150); len(got) != 2 || !near(got[1], want) {
		t.Fatalf("buys %v, want 50 then %v", got, want)
	}

	closeDeal(t, ex, "tp", 1)
	balance := ex.usdt
	tradeAt(t, b, s, 2)
	if st := b.state.get("SOLUSDT-L"); st.Losses != 0 || st.Loss != 0 {
		t.Fatalf("state after TP: %+v", st)
	}
	if got := stakes(t, ex); len(got) != 3 || !near(got[2], balance*0.05) {
		t.Fatalf("buys %v, want the base stake %v after TP", got, balance*0.05)
	}
}

func TestMartingaleStopsAfterMaxLosses(t *testing.T) {
	ex := newFakeExchange(t)
	b := newMartingaleBot(t, ex, filepath.Join(t.TempDir(), "state.json"))
	s := martingaleStrategy(1)

	tradeAt(t, b, s, 0)
	closeDeal(t, ex, "sl", 0)
	tradeAt(t, b, s, 1)
	tradeAt(t, b, s, 2)

	if got := stakes(t, ex); len(got) != 1 {
		t.Fatalf("buys %v: a stopped strategy must not enter", got)
	}
	if st := b.state.get("SOLUSDT-L"); st.Stopped == "" {
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
	closeDeal(t, ex, "sl", 0)
	ex.usdt = 60 // the next stake is ~95 USDT
	tradeAt(t, b, s, 1)

	if got := stakes(t, ex); len(got) != 1 {
		t.Fatalf("buys %v", got)
	}
	if st := b.state.get("SOLUSDT-L"); !strings.Contains(st.Stopped, "balance") {
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

	closeDeal(t, ex, "sl", 0)
	restarted := newMartingaleBot(t, ex, path)
	tradeAt(t, restarted, martingaleStrategy(5), 1)

	if st := restarted.state.get("SOLUSDT-L"); st.Losses != 1 || st.Open == nil {
		t.Fatalf("state after restart: %+v", st)
	}
	if got := stakes(t, ex); len(got) != 2 || got[1] <= 50 {
		t.Fatalf("buys %v, want a bigger stake after the SL", got)
	}
}

func TestMartingaleUnknownExitResetsSeries(t *testing.T) {
	ex := newFakeExchange(t)
	b := newMartingaleBot(t, ex, filepath.Join(t.TempDir(), "state.json"))
	s := martingaleStrategy(5)

	tradeAt(t, b, s, 0)
	closeDeal(t, ex, "sl", 0)
	tradeAt(t, b, s, 1)
	ex.positions[1] = 0 // closed by hand: no TP/SL of this deal in the history
	tradeAt(t, b, s, 2)

	if st := b.state.get("SOLUSDT-L"); st.Losses != 0 || st.Loss != 0 {
		t.Fatalf("state %+v, want a reset series", st)
	}
	if sent := b.notify.(*fakeNotifier).sent; !strings.Contains(strings.Join(sent, "\n"), "серия мартингейла сброшена") {
		t.Errorf("notifications %q", sent)
	}
}

// A short loses when the price rises to its SL: the loss is qty × (SL − entry),
// the same as a long's qty × (entry − SL).
func TestMartingaleShortStopLoss(t *testing.T) {
	ex := newFakeExchange(t)
	b := newMartingaleBot(t, ex, filepath.Join(t.TempDir(), "state.json"))
	s := short(martingaleStrategy(5))

	tradeAt(t, b, s, 0)
	closeDeal(t, ex, "sl", 0)
	tradeAt(t, b, s, 1)

	loss := 0.3333 * 15.0
	if st := b.state.get("SOLUSDT-S"); st.Losses != 1 || math.Abs(st.Loss-loss) > 1e-9 {
		t.Fatalf("state %+v, want loss %v", st, loss)
	}
	if got := stakes(t, ex); len(got) != 2 || !near(got[1], 50+1.2*loss/(20.0/150)) {
		t.Fatalf("stakes %v", got)
	}
	if ex.positions[2] <= 0 || ex.positions[1] != 0 {
		t.Fatalf("positions %v: want only a short", ex.positions)
	}
}

// The spot version stored the series under the symbol; a stop it set must
// survive the upgrade, while its open spot deal is dropped.
func TestMartingaleStateFromSpotVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	old := `{"SOLUSDT": {"base": 50, "loss": 20, "losses": 5, "stopped": "5 stop losses in a row",
		"open": {"link_id": "tb-SOLUSDT-1", "spent": 50, "qty": 0.33, "sl_price": 135}}}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	ex := newFakeExchange(t)
	b := newMartingaleBot(t, ex, path)

	st := b.state.get("SOLUSDT-L")
	if st.Losses != 5 || st.Stopped == "" || st.Open != nil {
		t.Fatalf("migrated state %+v", st)
	}
	tradeAt(t, b, martingaleStrategy(5), 0)
	if len(ex.placed) != 0 {
		t.Fatalf("placed %+v: a strategy stopped before the upgrade must stay stopped", ex.placed)
	}
}
