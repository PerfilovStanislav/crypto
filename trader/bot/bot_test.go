package bot

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"trader/config"
	"trader/db"
	"trader/exchange"
)

// fakeExchange simulates a Bybit spot account: a market buy fills at
// fillPrice and credits the base coin minus the fee.
type fakeExchange struct {
	klines     []db.Candle
	inst       exchange.Instrument
	balances   map[string]float64
	stopOrders []exchange.Order
	fillPrice  float64
	buyFee     float64 // share of the bought base coin taken as fee

	placed    []exchange.OrderRequest
	cancelled []string
	filled    map[string]exchange.Order
}

func newFakeExchange(t *testing.T) *fakeExchange {
	t.Helper()
	inst, err := exchange.NewInstrument("SOL", "USDT", "0.01", "0.0001", "0.000001", "0.0001", "5")
	if err != nil {
		t.Fatal(err)
	}
	return &fakeExchange{
		inst:      inst,
		balances:  map[string]float64{"USDT": 1000},
		fillPrice: 150.123,
		buyFee:    0.001,
		filled:    map[string]exchange.Order{},
	}
}

func (f *fakeExchange) GetKlines(symbol, timeframe string, limit int) ([]db.Candle, error) {
	return f.klines, nil
}

func (f *fakeExchange) GetInstrument(symbol string) (exchange.Instrument, error) {
	return f.inst, nil
}

func (f *fakeExchange) GetCoinBalance(coin string) (float64, error) {
	return f.balances[coin], nil
}

func (f *fakeExchange) PlaceOrder(req exchange.OrderRequest) (string, error) {
	f.placed = append(f.placed, req)
	id := req.OrderLinkID
	if req.Side == "Buy" && req.OrderFilter == "Order" {
		amount, err := strconv.ParseFloat(req.Qty, 64)
		if err != nil {
			return "", err
		}
		qty := amount / f.fillPrice
		f.balances["USDT"] -= amount
		f.balances["SOL"] += qty * (1 - f.buyFee)
		f.filled[id] = exchange.Order{OrderID: id, OrderLinkID: id, Status: "Filled", AvgPrice: f.fillPrice, CumExecQty: qty}
	}
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
	return nil
}

type fakeStore struct{ saved []db.Candle }

func (s *fakeStore) SaveCandles(ctx context.Context, candles []db.Candle) error {
	s.saved = append(s.saved, candles...)
	return nil
}

var testNow = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

func newTestBot(ex Exchange, store CandleStore) *Bot {
	b := New(&config.Config{}, store, ex)
	b.now = func() time.Time { return testNow }
	b.orderPoll = 0
	b.orderRetries = 2
	return b
}

// The old bot checked open orders, which are gone once a market buy fills,
// and bought again on the next signal.
func TestTradeSkipsWhileHoldingPosition(t *testing.T) {
	ex := newFakeExchange(t)
	ex.balances["SOL"] = 0.3327
	ex.stopOrders = []exchange.Order{{OrderID: "sl", OrderLinkID: "tb-SOLUSDT-1-sl", Status: "Untriggered"}}
	b := newTestBot(ex, &fakeStore{})

	if err := b.trade(strategy(sma5, sma2), true, 150); err != nil {
		t.Fatal(err)
	}
	if len(ex.placed) != 0 || len(ex.cancelled) != 0 {
		t.Fatalf("placed %v, cancelled %v: want no actions while the position is open", ex.placed, ex.cancelled)
	}
}

func TestTradeEntersWithSeparateStopOrders(t *testing.T) {
	ex := newFakeExchange(t)
	ex.balances["SOL"] = 0.00004 // dust below minOrderQty is not a position
	ex.stopOrders = []exchange.Order{
		{OrderID: "old-tp", OrderLinkID: "tb-SOLUSDT-1-tp", Status: "Untriggered"},
		{OrderID: "manual", OrderLinkID: "placed-by-hand", Status: "Untriggered"},
	}
	b := newTestBot(ex, &fakeStore{})

	if err := b.trade(strategy(sma5, sma2), true, 150); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(ex.cancelled, []string{"old-tp"}) {
		t.Errorf("cancelled %v, want only the bot's stale exit", ex.cancelled)
	}

	link := "tb-SOLUSDT-" + strconv.FormatInt(testNow.UnixMilli(), 10)
	// 5% of 1000 USDT; bought 50/150.123 SOL minus 0.1% fee plus dust = 0.33277 -> 0.3327
	// TP = 150.123 + 2000/10^2, SL = 150.123 - 1500/10^2, rounded to the 0.01 tick
	want := []exchange.OrderRequest{
		exchange.MarketBuyByQuote("SOLUSDT", "50.000000", link+"-buy"),
		exchange.StopMarketSell("SOLUSDT", "0.3327", "135.12", link+"-sl"),
		exchange.StopMarketSell("SOLUSDT", "0.3327", "170.12", link+"-tp"),
	}
	if !reflect.DeepEqual(ex.placed, want) {
		t.Errorf("placed:\n%+v\nwant:\n%+v", ex.placed, want)
	}
}

func TestTradeWithoutSignalOnlyCleansUp(t *testing.T) {
	ex := newFakeExchange(t)
	ex.stopOrders = []exchange.Order{{OrderID: "old-sl", OrderLinkID: "tb-SOLUSDT-1-sl"}}
	b := newTestBot(ex, &fakeStore{})

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

func TestEnterRefusesAmountBelowMinimum(t *testing.T) {
	ex := newFakeExchange(t)
	ex.balances["USDT"] = 90 // 5% = 4.5 < minOrderAmt 5
	b := newTestBot(ex, &fakeStore{})

	if err := b.trade(strategy(sma5, sma2), true, 150); err == nil {
		t.Fatal("want error")
	}
	if len(ex.placed) != 0 {
		t.Errorf("placed %v", ex.placed)
	}
}

func TestEnterRefusesNonPositiveStopLoss(t *testing.T) {
	ex := newFakeExchange(t)
	b := newTestBot(ex, &fakeStore{})

	s := strategy(sma5, sma2)
	zero := 0
	s.Decimals = &zero // 1500 USDT below a 150 USDT price
	if err := b.trade(s, true, 150); err == nil {
		t.Fatal("want error")
	}
	if len(ex.placed) != 0 {
		t.Errorf("placed %v", ex.placed)
	}
}

func TestEnterReportsUnprotectedPosition(t *testing.T) {
	ex := newFakeExchange(t)
	b := newTestBot(&rejectStops{ex}, &fakeStore{})

	err := b.trade(strategy(sma5, sma2), true, 150)
	if err == nil || !strings.Contains(err.Error(), "UNPROTECTED POSITION") {
		t.Fatalf("err = %v", err)
	}
}

// rejectStops lets the market buy through and rejects conditional orders.
type rejectStops struct{ *fakeExchange }

func (r *rejectStops) PlaceOrder(req exchange.OrderRequest) (string, error) {
	if req.OrderFilter == "StopOrder" {
		return "", errors.New("retCode 170131")
	}
	return r.fakeExchange.PlaceOrder(req)
}

func TestProcessCandleWaitsForRolloverThenTrades(t *testing.T) {
	ex := newFakeExchange(t)
	store := &fakeStore{}
	b := newTestBot(ex, store)
	s := strategy(sma5, sma2)

	// uptrend: indicator2 > indicator1 on every candle
	all := makeCandles(series(300, func(i int) float64 { return 100 + float64(i)/10 }))
	expected := all[298].Timestamp

	// Bybit still lists the expected candle as the newest one
	ex.klines = all[:299]
	done, err := b.processCandle(context.Background(), s, expected)
	if err != nil || done {
		t.Fatalf("before rollover: done=%v err=%v", done, err)
	}
	if len(store.saved) != 0 || len(ex.placed) != 0 {
		t.Fatal("nothing must happen before the candle is closed")
	}

	ex.klines = all
	done, err = b.processCandle(context.Background(), s, expected)
	if err != nil || !done {
		t.Fatalf("after rollover: done=%v err=%v", done, err)
	}
	if len(store.saved) != 1 || !store.saved[0].Timestamp.Equal(expected) || store.saved[0].Timeframe != "4h" {
		t.Errorf("saved %+v", store.saved)
	}
	if len(ex.placed) != 3 || ex.placed[0].Side != "Buy" {
		t.Fatalf("placed %+v, want buy + sl + tp", ex.placed)
	}

	// Next candle: the signal still holds, but the position is open
	ex.placed = nil
	done, err = b.processCandle(context.Background(), s, expected) // same data, position now held
	if err != nil || !done || len(ex.placed) != 0 {
		t.Fatalf("second pass: done=%v err=%v placed=%v", done, err, ex.placed)
	}
}
