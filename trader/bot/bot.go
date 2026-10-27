package bot

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"trader/config"
	"trader/exchange"
)

// Exchange is the part of the Bybit client the bot uses.
type Exchange interface {
	GetKlines(symbol, timeframe string, limit int) ([]exchange.Candle, error)
	GetInstrument(symbol string) (exchange.Instrument, error)
	GetFreeBalance(coin string) (float64, error)
	SetupHedge(symbol string) error
	GetPositionSize(symbol string, positionIdx int) (float64, error)
	PlaceOrder(req exchange.OrderRequest) (string, error)
	GetClosedOrder(symbol, orderID string) (exchange.Order, bool, error)
	GetOpenStopOrders(symbol string) ([]exchange.Order, error)
	CancelStopOrder(symbol, orderID string) error
}

// Notifier delivers messages to the user (Telegram).
type Notifier interface {
	Notify(text string) error
}

const (
	klineLimit = 1000 // Bybit maximum per request

	// orderLinkPrefix marks the bot's orders, so it never cancels orders
	// placed by hand.
	orderLinkPrefix = "tb-"
)

type Bot struct {
	cfg    *config.Config
	ex     Exchange
	notify Notifier
	state  *stateStore // martingale series; loaded in Run when a strategy uses it

	now           func() time.Time
	candlePoll    time.Duration // wait between attempts to get the closed candle
	candleRetries int
	orderPoll     time.Duration // wait between checks of the market entry status
	orderRetries  int
}

func New(cfg *config.Config, ex Exchange, notify Notifier) *Bot {
	return &Bot{
		cfg:           cfg,
		ex:            ex,
		notify:        notify,
		now:           time.Now,
		candlePoll:    time.Second,
		candleRetries: 60,
		orderPoll:     500 * time.Millisecond,
		orderRetries:  10,
	}
}

func (b *Bot) Run(ctx context.Context) error {
	log.Println("Starting trading bot...")

	if b.state == nil && usesMartingale(b.cfg) {
		st, err := loadState(b.cfg.StateFile)
		if err != nil {
			return fmt.Errorf("martingale state: %w", err)
		}
		b.state = st
	}

	// Before any order: in one-way mode the long and the short of a symbol
	// would be one position.
	ready := make(map[string]bool)
	for _, s := range b.cfg.Strategies {
		if ready[s.Symbol] {
			continue
		}
		if err := b.ex.SetupHedge(s.Symbol); err != nil {
			return err
		}
		ready[s.Symbol] = true
		log.Printf("[%s] hedge mode, leverage 1x", s.Symbol)
	}

	var wg sync.WaitGroup
	for _, strategy := range b.cfg.Strategies {
		wg.Add(1)
		go func(s config.StrategyConfig) {
			defer wg.Done()
			b.runStrategy(ctx, s)
		}(strategy)
	}

	wg.Wait()
	return nil
}

func (b *Bot) runStrategy(ctx context.Context, s config.StrategyConfig) {
	tfDuration, err := parseTimeframeDuration(s.Timeframe)
	if err != nil {
		b.logf(s, "Invalid timeframe: %v", err)
		return
	}

	for {
		closeTime := b.now().UTC().Truncate(tfDuration).Add(tfDuration)
		sleepDuration := closeTime.Sub(b.now())

		b.logf(s, "Sleeping %v until candle close at %v", sleepDuration, closeTime)
		if !sleepCtx(ctx, sleepDuration) {
			return
		}

		// Start time of the candle that has just closed
		expected := closeTime.Add(-tfDuration)
		for attempt := 1; ; attempt++ {
			done, err := b.processCandle(s, expected)
			if err != nil {
				b.logf(s, "Error processing candle %v: %v", expected, err)
			}
			if done {
				break
			}
			if attempt >= b.candleRetries {
				b.logf(s, "Candle %v not available after %d attempts, skipping it", expected, attempt)
				break
			}
			if !sleepCtx(ctx, b.candlePoll) {
				return
			}
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func parseTimeframeDuration(tf string) (time.Duration, error) {
	switch tf {
	case "1m":
		return time.Minute, nil
	case "3m":
		return 3 * time.Minute, nil
	case "5m":
		return 5 * time.Minute, nil
	case "15m":
		return 15 * time.Minute, nil
	case "30m":
		return 30 * time.Minute, nil
	case "1h":
		return time.Hour, nil
	case "2h":
		return 2 * time.Hour, nil
	case "4h":
		return 4 * time.Hour, nil
	case "6h":
		return 6 * time.Hour, nil
	case "12h":
		return 12 * time.Hour, nil
	case "1d":
		return 24 * time.Hour, nil
	}
	return 0, fmt.Errorf("unsupported timeframe: %s", tf)
}

// processCandle handles the candle that started at expected. done is false
// only while that candle is not available yet: once it is, the candle is never
// processed again, so a failed step can not lead to a second buy.
func (b *Bot) processCandle(s config.StrategyConfig, expected time.Time) (done bool, err error) {
	klines, err := b.ex.GetKlines(s.Symbol, s.Timeframe, klineLimit)
	if err != nil {
		return false, err
	}

	closed, ok := closedCandles(klines, expected)
	if !ok {
		return false, nil
	}
	last := closed[len(closed)-1]
	b.logf(s, "Candle %v closed at %v", last.Timestamp, last.Close)

	signal, err := Signal(closed, s)
	if err != nil {
		return true, fmt.Errorf("signal: %w", err)
	}

	// No ctx here on purpose: a shutdown must not interrupt an entry between
	// the market order and its stop loss.
	return true, b.trade(s, signal, last.Close)
}

// trade mirrors the backtest: at most one position, entered on the candle
// after any candle where the signal holds while no position is open. The
// position is the strategy's side of the hedge-mode position (long or short):
// no other strategy trades it.
func (b *Bot) trade(s config.StrategyConfig, signal bool, lastClose float64) error {
	inst, err := b.ex.GetInstrument(s.Symbol)
	if err != nil {
		return fmt.Errorf("instrument: %w", err)
	}

	size, err := b.ex.GetPositionSize(s.Symbol, positionIdx(s))
	if err != nil {
		return fmt.Errorf("position: %w", err)
	}
	if size > 0 {
		b.logf(s, "Position open (%s %s), waiting for TP/SL", inst.FormatQty(size), inst.BaseCoin)
		if exits, err := b.botStopOrders(s); err == nil && len(exits) == 0 {
			b.logf(s, "WARNING: position has no TP/SL orders of the bot, close it or add them by hand")
		}
		return nil
	}

	// Before cancelStaleExits: the exit left over shows how the deal closed.
	if s.Martingale > 0 {
		if err := b.settle(s); err != nil {
			return fmt.Errorf("martingale: %w", err)
		}
	}

	// The position is gone, so TP or SL has fired. The other one is still
	// waiting and would close the next position at a stale price.
	if err := b.cancelStaleExits(s); err != nil {
		return fmt.Errorf("cancel stale exit orders: %w", err)
	}

	if s.Martingale > 0 {
		if st := b.state.get(s.Key()); st.Stopped != "" {
			b.logf(s, "Martingale stopped (%s), remove %s from %s to resume", st.Stopped, s.Key(), b.state.path)
			return nil
		}
	}

	if !signal {
		b.logf(s, "No signal")
		return nil
	}

	return b.enter(s, inst, lastClose)
}

// positionIdx is the strategy's side of a hedge-mode position.
func positionIdx(s config.StrategyConfig) int {
	if s.Short() {
		return 2
	}
	return 1
}

// sides returns the order side that opens the strategy's position and the one
// that closes it.
func sides(s config.StrategyConfig) (open, close string) {
	if s.Short() {
		return "Sell", "Buy"
	}
	return "Buy", "Sell"
}

// title names the strategy in messages: "SOLUSDT 4h short".
func title(s config.StrategyConfig) string {
	side := "long"
	if s.Short() {
		side = "short"
	}
	return s.Symbol + " " + s.Timeframe + " " + side
}

// botStopOrders lists the strategy's untriggered conditional orders.
func (b *Bot) botStopOrders(s config.StrategyConfig) ([]exchange.Order, error) {
	orders, err := b.ex.GetOpenStopOrders(s.Symbol)
	if err != nil {
		return nil, err
	}
	var own []exchange.Order
	for _, o := range orders {
		if strings.HasPrefix(o.OrderLinkID, orderLinkPrefix+s.Key()+"-") {
			own = append(own, o)
		}
	}
	return own, nil
}

func (b *Bot) cancelStaleExits(s config.StrategyConfig) error {
	orders, err := b.botStopOrders(s)
	if err != nil {
		return err
	}
	for _, o := range orders {
		if err := b.ex.CancelStopOrder(s.Symbol, o.OrderID); err != nil {
			return fmt.Errorf("order %s: %w", o.OrderID, err)
		}
		b.logf(s, "Cancelled stale exit order %s (%s)", o.OrderID, o.OrderLinkID)
	}
	return nil
}

// enter opens the strategy's side with deposit_percent of the free USDT balance
// (leverage 1x: the order value is the margin), then protects it with two
// conditional reduce-only market orders, the stop loss first.
func (b *Bot) enter(s config.StrategyConfig, inst exchange.Instrument, lastClose float64) (err error) {
	decimals := *s.Decimals
	idx := positionIdx(s)
	openSide, closeSide := sides(s)

	// Once money has moved, a failure has to reach the user, not only the log.
	opened := false
	defer func() {
		if opened && err != nil {
			b.notifyf(s, "⚠️ %s: вход сорвался после открытия позиции, проверьте её: %v", title(s), err)
		}
	}()

	// Refuse settings that can not work before any money moves.
	if tp, sl := TpSlPrices(lastClose, s.TakeProfit, s.StopLoss, decimals, s.Short()); tp <= 0 || sl <= 0 {
		return fmt.Errorf("take_profit %v / stop_loss %v with decimals %d give a non-positive price for %v", s.TakeProfit, s.StopLoss, decimals, lastClose)
	}

	balance, err := b.ex.GetFreeBalance(inst.QuoteCoin)
	if err != nil {
		return fmt.Errorf("%s balance: %w", inst.QuoteCoin, err)
	}
	stake := balance * s.DepositPercent / 100
	var base float64
	if s.Martingale > 0 {
		var ok bool
		if stake, base, ok, err = b.martingaleStake(s, balance, lastClose); !ok || err != nil {
			return err
		}
	}
	qty := inst.FloorQty(stake / lastClose)
	// Bybit rejects orders below minOrderQty or worth less than minNotionalValue
	// (5 USDT), so a small share of the balance is raised to the minimum while
	// the balance covers it. More than twice the stake would put far more of
	// the balance into one trade than deposit_percent says (BTCUSDT: 0.001 BTC
	// is ~80 USDT): such an entry is skipped.
	if minQty := inst.MinQty(lastClose); qty < minQty {
		if minValue := minQty * lastClose; minValue > 2*stake {
			b.logf(s, "Skipping entry: Bybit minimum order %s %s (%.2f %s) is more than twice the stake %.2f %s",
				inst.FormatQty(minQty), inst.BaseCoin, minValue, inst.QuoteCoin, stake, inst.QuoteCoin)
			return nil
		}
		b.logf(s, "Order of %s %s is below the minimum, opening %s", inst.FormatQty(qty), inst.BaseCoin, inst.FormatQty(minQty))
		qty = minQty
	}
	if qty <= 0 || qty*lastClose > balance {
		return fmt.Errorf("%s balance %.2f is below the order value %.2f", inst.QuoteCoin, balance, qty*lastClose)
	}

	// Bybit caps orderLinkId at 36 characters: milliseconds in base 36 (8 digits)
	// and short suffixes keep even "tb-1000000BABYDOGEUSDT-L-…-sl" within it.
	linkID := orderLinkPrefix + s.Key() + "-" + strconv.FormatInt(b.now().UnixMilli(), 36)

	b.logf(s, "Signal: market %s %s %s (%.2f %s)", strings.ToLower(openSide), inst.FormatQty(qty), inst.BaseCoin, qty*lastClose, inst.QuoteCoin)
	orderID, err := b.ex.PlaceOrder(exchange.MarketOrder(s.Symbol, openSide, inst.FormatQty(qty), idx, false, linkID+"-o"))
	if err != nil {
		return fmt.Errorf("market %s: %w", strings.ToLower(openSide), err)
	}
	opened = true

	// The backtest enters at the next candle open, which is the market price
	// right now; the actual average fill price is the closest equivalent.
	entry := lastClose
	order, err := b.waitClosed(s.Symbol, orderID)
	switch {
	case err != nil:
		b.logf(s, "WARNING: status of order %s unknown (%v), using last close %v as entry price", orderID, err, lastClose)
	case order.CumExecQty <= 0:
		return fmt.Errorf("market order %s not filled: %s", orderID, order.Status)
	default:
		entry = order.AvgPrice
	}

	// The side belongs to this strategy alone: its size is exactly what to protect.
	size, err := b.ex.GetPositionSize(s.Symbol, idx)
	if err != nil {
		return fmt.Errorf("UNPROTECTED POSITION: position size after order %s: %w", orderID, err)
	}
	if size <= 0 {
		return fmt.Errorf("no position to protect after order %s", orderID)
	}
	posQty := inst.FormatQty(size)

	tp, sl := TpSlPrices(entry, s.TakeProfit, s.StopLoss, decimals, s.Short())
	tpPrice, slPrice := inst.FormatPrice(tp), inst.FormatPrice(sl)
	tpDir, slDir := exchange.TriggerRise, exchange.TriggerFall
	if s.Short() {
		tpDir, slDir = slDir, tpDir
	}

	// Stop loss first: if the take profit fails, the loss is still capped.
	if _, err := b.ex.PlaceOrder(exchange.StopMarket(s.Symbol, closeSide, posQty, slPrice, slDir, idx, linkID+"-sl")); err != nil {
		// A position without a stop must not stay open.
		if _, cerr := b.ex.PlaceOrder(exchange.MarketOrder(s.Symbol, closeSide, posQty, idx, true, linkID+"-c")); cerr != nil {
			return fmt.Errorf("UNPROTECTED POSITION: stop loss at %s: %w; closing at market: %v", slPrice, err, cerr)
		}
		return fmt.Errorf("stop loss at %s: %w; the position was closed at market", slPrice, err)
	}
	if s.Martingale > 0 {
		deal := &openDeal{LinkID: linkID, Entry: entry, Qty: size, SLPrice: sl}
		if err := b.state.update(s.Key(), func(st *seriesState) { st.Base, st.Open = base, deal }); err != nil {
			b.notifyf(s, "⚠️ %s: не удалось сохранить состояние мартингейла (%v), исход этой сделки не попадёт в серию", title(s), err)
		}
	}
	if _, err := b.ex.PlaceOrder(exchange.StopMarket(s.Symbol, closeSide, posQty, tpPrice, tpDir, idx, linkID+"-tp")); err != nil {
		return fmt.Errorf("take profit at %s: %w", tpPrice, err)
	}

	verb := "куплено"
	if s.Short() {
		verb = "продано"
	}
	b.logf(s, "Entered %s %s at %v: TP %s, SL %s", posQty, inst.BaseCoin, entry, tpPrice, slPrice)
	b.notifyf(s, "🟢 %s: открыта сделка\n%s %s %s по %v\nTP %s, SL %s", title(s), verb, posQty, inst.BaseCoin, entry, tpPrice, slPrice)
	return nil
}

// notifyf sends a message; a failed notification must never stop trading.
func (b *Bot) notifyf(s config.StrategyConfig, format string, args ...any) {
	if b.notify == nil {
		return
	}
	if err := b.notify.Notify(fmt.Sprintf(format, args...)); err != nil {
		b.logf(s, "Notification failed: %v", err)
	}
}

// waitClosed polls until the order reaches a final status. A market order
// normally closes within milliseconds.
func (b *Bot) waitClosed(symbol, orderID string) (exchange.Order, error) {
	var lastErr error
	for i := 0; i < b.orderRetries; i++ {
		if i > 0 {
			time.Sleep(b.orderPoll)
		}
		order, found, err := b.ex.GetClosedOrder(symbol, orderID)
		if err != nil {
			lastErr = err
			continue
		}
		if found && order.Closed() {
			return order, nil
		}
	}
	if lastErr != nil {
		return exchange.Order{}, lastErr
	}
	return exchange.Order{}, fmt.Errorf("order %s not closed after %d checks", orderID, b.orderRetries)
}

func (b *Bot) logf(s config.StrategyConfig, format string, args ...any) {
	log.Printf("[%s] "+format, append([]any{title(s)}, args...)...)
}
