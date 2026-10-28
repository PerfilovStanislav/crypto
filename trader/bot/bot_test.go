package bot

import (
	"context"
	"errors"
	"log"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"trader/config"
	"trader/exchange"
)

// fakeExchange simulates a Bybit unified account trading USDT perpetuals in
// hedge mode: a market order fills at fillPrice and changes the size of its
// side; conditional orders wait in stopOrders until a test fires them.
type fakeExchange struct {
	klines     []exchange.Candle
	inst       exchange.Instrument
	usdt       float64
	positions  map[int]float64 // positionIdx -> size
	stopOrders []exchange.Order
	fillPrice  float64

	placed    []exchange.OrderRequest
	cancelled []string
	filled    map[string]exchange.Order
	hedged    []string
}

func newFakeExchange(t *testing.T) *fakeExchange {
	t.Helper()
	inst, err := exchange.NewInstrument("SOL", "USDT", "0.01", "0.0001", "0.0001", "5")
	if err != nil {
		t.Fatal(err)
	}
	return &fakeExchange{
		inst:      inst,
		usdt:      1000,
		positions: map[int]float64{},
		fillPrice: 150.123,
		filled:    map[string]exchange.Order{},
	}
}

func (f *fakeExchange) GetKlines(symbol, timeframe string, limit int) ([]exchange.Candle, error) {
	return f.klines, nil
}

func (f *fakeExchange) GetInstrument(symbol string) (exchange.Instrument, error) {
	return f.inst, nil
}

func (f *fakeExchange) GetFreeBalance(coin string) (float64, error) {
	return f.usdt, nil
}

func (f *fakeExchange) SetupHedge(symbol string) error {
	f.hedged = append(f.hedged, symbol)
	return nil
}

func (f *fakeExchange) GetPositionSize(symbol string, positionIdx int) (float64, error) {
	return f.positions[positionIdx], nil
}

func (f *fakeExchange) PlaceOrder(req exchange.OrderRequest) (string, error) {
	f.placed = append(f.placed, req)
	id := req.OrderLinkID
	if req.TriggerPrice != "" {
		f.stopOrders = append(f.stopOrders, exchange.Order{OrderID: id, OrderLinkID: id, Status: "Untriggered"})
		return id, nil
	}
	qty, err := strconv.ParseFloat(req.Qty, 64)
	if err != nil {
		return "", err
	}
	if req.ReduceOnly {
		qty = -qty
	}
	f.positions[req.PositionIdx] += qty
	f.filled[id] = exchange.Order{OrderID: id, OrderLinkID: id, Status: "Filled", AvgPrice: f.fillPrice, CumExecQty: max(qty, -qty)}
	return id, nil
}

func (f *fakeExchange) GetClosedOrder(symbol, orderID string) (exchange.Order, bool, error) {
	o, ok := f.filled[orderID]
	return o, ok, nil
}

func (f *fakeExchange) GetOpenStopOrders(symbol string) ([]exchange.Order, error) {
	return f.stopOrders, nil
}

func (f *fakeExchange) CancelStopOrder(symbol, orderID string) error {
	f.cancelled = append(f.cancelled, orderID)
	for i, o := range f.stopOrders {
		if o.OrderID == orderID {
			f.stopOrders = append(f.stopOrders[:i:i], f.stopOrders[i+1:]...)
			break
		}
	}
	return nil
}

var testNow = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

type fakeNotifier struct{ sent []string }

func (n *fakeNotifier) Notify(text string) error {
	n.sent = append(n.sent, text)
	return nil
}

func newTestBot(ex Exchange) *Bot {
	b := New(&config.Config{}, ex, &fakeNotifier{})
	b.now = func() time.Time { return testNow }
	b.orderPoll = 0
	b.orderRetries = 2
	return b
}

func short(s config.StrategyConfig) config.StrategyConfig {
	s.Direction = "sell"
	return s
}

var link = strconv.FormatInt(testNow.UnixMilli(), 36)

func TestTradeSkipsWhileHoldingPosition(t *testing.T) {
	ex := newFakeExchange(t)
	ex.positions[1] = 0.3331
	ex.stopOrders = []exchange.Order{{OrderID: "sl", OrderLinkID: "tb-SOLUSDT-L-1-sl", Status: "Untriggered"}}
	b := newTestBot(ex)

	if err := b.trade(strategy(sma5, sma2), true, 150); err != nil {
		t.Fatal(err)
	}
	if len(ex.placed) != 0 || len(ex.cancelled) != 0 {
		t.Fatalf("placed %v, cancelled %v: want no actions while the position is open", ex.placed, ex.cancelled)
	}
}

func TestTradeEntersLongWithSeparateStopOrders(t *testing.T) {
	ex := newFakeExchange(t)
	ex.stopOrders = []exchange.Order{
		{OrderID: "old-tp", OrderLinkID: "tb-SOLUSDT-L-1-tp", Status: "Untriggered"},
		{OrderID: "short-sl", OrderLinkID: "tb-SOLUSDT-S-1-sl", Status: "Untriggered"},
		{OrderID: "manual", OrderLinkID: "placed-by-hand", Status: "Untriggered"},
	}
	b := newTestBot(ex)

	if err := b.trade(strategy(sma5, sma2), true, 150); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(ex.cancelled, []string{"old-tp"}) {
		t.Errorf("cancelled %v, want only the long's stale exit", ex.cancelled)
	}

	// 5% of 1000 USDT at 150 = 0.3333 SOL; TP = 150.123 + 2000/10^2, SL = 150.123 - 1500/10^2.
	want := []exchange.OrderRequest{
		exchange.MarketOrder("SOLUSDT", "Buy", "0.3333", 1, false, "tb-SOLUSDT-L-"+link+"-o"),
		exchange.StopMarket("SOLUSDT", "Sell", "0.3333", "135.12", exchange.TriggerFall, 1, "tb-SOLUSDT-L-"+link+"-sl"),
		exchange.StopMarket("SOLUSDT", "Sell", "0.3333", "170.12", exchange.TriggerRise, 1, "tb-SOLUSDT-L-"+link+"-tp"),
	}
	if !reflect.DeepEqual(ex.placed, want) {
		t.Errorf("placed:\n%+v\nwant:\n%+v", ex.placed, want)
	}

	sent := b.notify.(*fakeNotifier).sent
	if len(sent) != 1 || !strings.Contains(sent[0], "SOLUSDT 4h long: открыта сделка") || !strings.Contains(sent[0], "TP 170.12, SL 135.12") {
		t.Errorf("notifications %q", sent)
	}
}

func TestTradeEntersShort(t *testing.T) {
	ex := newFakeExchange(t)
	b := newTestBot(ex)

	if err := b.trade(short(strategy(sma5, sma2)), true, 150); err != nil {
		t.Fatal(err)
	}

	// A short: sell to open, TP below the entry, SL above, both close with a buy.
	want := []exchange.OrderRequest{
		exchange.MarketOrder("SOLUSDT", "Sell", "0.3333", 2, false, "tb-SOLUSDT-S-"+link+"-o"),
		exchange.StopMarket("SOLUSDT", "Buy", "0.3333", "165.12", exchange.TriggerRise, 2, "tb-SOLUSDT-S-"+link+"-sl"),
		exchange.StopMarket("SOLUSDT", "Buy", "0.3333", "130.12", exchange.TriggerFall, 2, "tb-SOLUSDT-S-"+link+"-tp"),
	}
	if !reflect.DeepEqual(ex.placed, want) {
		t.Errorf("placed:\n%+v\nwant:\n%+v", ex.placed, want)
	}
	if sent := b.notify.(*fakeNotifier).sent; len(sent) != 1 || !strings.Contains(sent[0], "SOLUSDT 4h short") || !strings.Contains(sent[0], "продано") {
		t.Errorf("notifications %q", sent)
	}
}

// Each strategy waits only for its own side: an open long does not block the
// short, and the long's exits are not the short's to cancel.
func TestLongAndShortTradeIndependently(t *testing.T) {
	ex := newFakeExchange(t)
	b := newTestBot(ex)
	long, sell := strategy(sma5, sma2), short(strategy(sma5, sma2))

	if err := b.trade(long, true, 150); err != nil {
		t.Fatal(err)
	}
	if err := b.trade(sell, true, 150); err != nil {
		t.Fatal(err)
	}
	if len(ex.placed) != 6 || len(ex.cancelled) != 0 {
		t.Fatalf("placed %d orders, cancelled %v: want both entries, nothing cancelled", len(ex.placed), ex.cancelled)
	}
	if ex.positions[1] != 0.3333 || ex.positions[2] != 0.3333 {
		t.Fatalf("positions %v", ex.positions)
	}

	// Both are in a trade now: another signal opens nothing.
	ex.placed = nil
	for _, s := range []config.StrategyConfig{long, sell} {
		if err := b.trade(s, true, 150); err != nil {
			t.Fatal(err)
		}
	}
	if len(ex.placed) != 0 {
		t.Fatalf("placed %+v while both positions are open", ex.placed)
	}
}

func TestTradeWithoutSignalOnlyCleansUp(t *testing.T) {
	ex := newFakeExchange(t)
	ex.stopOrders = []exchange.Order{{OrderID: "old-sl", OrderLinkID: "tb-SOLUSDT-L-1-sl"}}
	b := newTestBot(ex)

	if err := b.trade(strategy(sma5, sma2), false, 150); err != nil {
		t.Fatal(err)
	}
	if len(ex.placed) != 0 {
		t.Errorf("placed %v without a signal", ex.placed)
	}
	if !reflect.DeepEqual(ex.cancelled, []string{"old-sl"}) {
		t.Errorf("cancelled %v", ex.cancelled)
	}
}

func TestEnterRaisesAmountToMinimum(t *testing.T) {
	ex := newFakeExchange(t)
	ex.usdt = 90 // 5% = 4.5 USDT < minNotionalValue 5
	b := newTestBot(ex)

	if err := b.trade(strategy(sma5, sma2), true, 150); err != nil {
		t.Fatal(err)
	}
	// 5 USDT at 150 is 0.03333 SOL, up to the step: 0.0334.
	if len(ex.placed) == 0 || ex.placed[0].Qty != "0.0334" {
		t.Fatalf("placed %+v, want an order for the minimum 5 USDT", ex.placed)
	}
}

func TestEnterSkipsMinimumFarAboveStake(t *testing.T) {
	ex := newFakeExchange(t)
	ex.usdt = 40 // 5% = 2 USDT, the minimum order is 5.01 USDT
	b := newTestBot(ex)
	var logs strings.Builder
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	if err := b.trade(strategy(sma5, sma2), true, 150); err != nil {
		t.Fatal(err)
	}
	if len(ex.placed) != 0 {
		t.Errorf("placed %+v, want the entry skipped", ex.placed)
	}
	if !strings.Contains(logs.String(), "Skipping entry: Bybit minimum order 0.0334 SOL (5.01 USDT) is more than twice the stake 2.00 USDT") {
		t.Errorf("log %q", logs.String())
	}
	if sent := b.notify.(*fakeNotifier).sent; len(sent) != 1 || !strings.Contains(sent[0], "SOLUSDT 4h long: вход пропущен") || !strings.Contains(sent[0], "5.01 USDT") {
		t.Errorf("notifications %q", sent)
	}
}

func TestEnterRefusesBalanceBelowMinimum(t *testing.T) {
	ex := newFakeExchange(t)
	ex.usdt = 4 // even the minimum 5 USDT can not be opened
	b := newTestBot(ex)
	s := strategy(sma5, sma2)
	s.DepositPercent = 100 // the minimum is within twice the stake, but above the balance

	if err := b.trade(s, true, 150); err == nil {
		t.Fatal("want error")
	}
	if len(ex.placed) != 0 {
		t.Errorf("placed %v", ex.placed)
	}
	// Nothing was opened: no message, or it would repeat on every candle.
	if sent := b.notify.(*fakeNotifier).sent; len(sent) != 0 {
		t.Errorf("notifications %q", sent)
	}
}

func TestEnterRefusesNonPositivePrices(t *testing.T) {
	zero := 0
	for _, s := range []config.StrategyConfig{strategy(sma5, sma2), short(strategy(sma5, sma2))} {
		ex := newFakeExchange(t)
		b := newTestBot(ex)
		s.Decimals = &zero // offsets of 1500 and 2000 USDT from a 150 USDT price
		if err := b.trade(s, true, 150); err == nil {
			t.Fatalf("%s: want error", title(s))
		}
		if len(ex.placed) != 0 {
			t.Errorf("%s: placed %v", title(s), ex.placed)
		}
	}
}

func TestEnterClosesPositionWithoutStopLoss(t *testing.T) {
	ex := newFakeExchange(t)
	b := newTestBot(&rejectStops{fakeExchange: ex})

	err := b.trade(strategy(sma5, sma2), true, 150)
	if err == nil || !strings.Contains(err.Error(), "closed at market") {
		t.Fatalf("err = %v", err)
	}
	last := ex.placed[len(ex.placed)-1]
	if want := exchange.MarketOrder("SOLUSDT", "Sell", "0.3333", 1, true, "tb-SOLUSDT-L-"+link+"-c"); !reflect.DeepEqual(last, want) {
		t.Errorf("last order %+v, want a reduce-only market close", last)
	}
	if ex.positions[1] != 0 {
		t.Errorf("long position %v left open", ex.positions[1])
	}
	if sent := b.notify.(*fakeNotifier).sent; len(sent) != 1 || !strings.Contains(sent[0], "вход сорвался") {
		t.Errorf("notifications %q", sent)
	}

	// If even the close fails, the message must say the position is unprotected.
	ex = newFakeExchange(t)
	b = newTestBot(&rejectStops{fakeExchange: ex, rejectClose: true})
	err = b.trade(strategy(sma5, sma2), true, 150)
	if err == nil || !strings.Contains(err.Error(), "UNPROTECTED POSITION") {
		t.Fatalf("err = %v", err)
	}
}

// rejectStops lets the market entry through and rejects conditional orders
// (and, with rejectClose, reduce-only closes).
type rejectStops struct {
	*fakeExchange
	rejectClose bool
}

func (r *rejectStops) PlaceOrder(req exchange.OrderRequest) (string, error) {
	if req.TriggerPrice != "" || (r.rejectClose && req.ReduceOnly) {
		return "", errors.New("retCode 170131")
	}
	return r.fakeExchange.PlaceOrder(req)
}

func TestRunSetsUpHedgeOncePerSymbol(t *testing.T) {
	ex := newFakeExchange(t)
	b := newTestBot(ex)
	b.cfg = &config.Config{Strategies: []config.StrategyConfig{
		strategy(sma5, sma2),
		short(strategy(sma5, sma2)),
		func() config.StrategyConfig { s := strategy(sma5, sma2); s.Symbol = "BTCUSDT"; return s }(),
	}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // strategies stop at their first wait
	if err := b.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ex.hedged, []string{"SOLUSDT", "BTCUSDT"}) {
		t.Errorf("hedge setup for %v", ex.hedged)
	}

	if err := newTestBot(&failHedge{ex}).Run(ctx); err != nil {
		t.Fatalf("no strategies, no setup: %v", err)
	}
	failing := newTestBot(&failHedge{ex})
	failing.cfg = b.cfg
	if err := failing.Run(ctx); err == nil {
		t.Fatal("want the bot to stop when hedge mode can not be set")
	}
}

type failHedge struct{ *fakeExchange }

func (failHedge) SetupHedge(string) error { return errors.New("retCode 110024") }

func TestProcessCandleWaitsForRolloverThenTrades(t *testing.T) {
	ex := newFakeExchange(t)
	b := newTestBot(ex)
	s := strategy(sma5, sma2)

	// uptrend: indicator2 > indicator1 on every candle
	all := makeCandles(series(300, func(i int) float64 { return 100 + float64(i)/10 }))
	expected := all[298].Timestamp

	// Bybit still lists the expected candle as the newest one
	ex.klines = all[:299]
	done, err := b.processCandle(s, expected)
	if err != nil || done {
		t.Fatalf("before rollover: done=%v err=%v", done, err)
	}
	if len(ex.placed) != 0 {
		t.Fatal("nothing must happen before the candle is closed")
	}

	ex.klines = all
	done, err = b.processCandle(s, expected)
	if err != nil || !done {
		t.Fatalf("after rollover: done=%v err=%v", done, err)
	}
	if len(ex.placed) != 3 || ex.placed[0].Side != "Buy" {
		t.Fatalf("placed %+v, want entry + sl + tp", ex.placed)
	}

	// Next candle: the signal still holds, but the position is open
	ex.placed = nil
	done, err = b.processCandle(s, expected) // same data, position now held
	if err != nil || !done || len(ex.placed) != 0 {
		t.Fatalf("second pass: done=%v err=%v placed=%v", done, err, ex.placed)
	}
}

// Bybit rejects an orderLinkId longer than 36 characters; the longest Bybit
// pair names have 19.
func TestOrderLinkIDsFitBybitLimit(t *testing.T) {
	s := strategy(sma5, sma2)
	s.Symbol = "1000000BABYDOGEUSDT"

	normal, failing := newFakeExchange(t), newFakeExchange(t)
	if err := newTestBot(normal).trade(s, true, 150); err != nil { // entry, SL, TP
		t.Fatal(err)
	}
	newTestBot(&rejectStops{fakeExchange: failing}).trade(s, true, 150) // entry, emergency close

	placed := append(normal.placed, failing.placed...)
	if len(placed) != 5 {
		t.Fatalf("placed %d orders, want 5", len(placed))
	}
	for _, o := range placed {
		if len(o.OrderLinkID) > 36 {
			t.Errorf("orderLinkId %q has %d characters", o.OrderLinkID, len(o.OrderLinkID))
		}
	}
}
