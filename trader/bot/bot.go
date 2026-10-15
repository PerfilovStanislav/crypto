package bot

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"trader/config"
	"trader/db"
	"trader/exchange"
)

// Exchange is the part of the Bybit client the bot uses.
type Exchange interface {
	GetKlines(symbol, timeframe string, limit int) ([]db.Candle, error)
	GetInstrument(symbol string) (exchange.Instrument, error)
	GetCoinBalance(coin string) (float64, error)
	PlaceOrder(req exchange.OrderRequest) (string, error)
	GetClosedOrder(symbol, orderID string) (exchange.Order, bool, error)
	GetOpenStopOrders(symbol string) ([]exchange.Order, error)
	CancelStopOrder(symbol, orderID string) error
}

type CandleStore interface {
	SaveCandles(ctx context.Context, candles []db.Candle) error
}

const (
	klineLimit = 1000 // Bybit maximum per request

	// orderLinkPrefix marks the bot's orders, so it never cancels orders
	// placed by hand.
	orderLinkPrefix = "tb-"
)

type Bot struct {
	cfg   *config.Config
	store CandleStore
	ex    Exchange

	now           func() time.Time
	candlePoll    time.Duration // wait between attempts to get the closed candle
	candleRetries int
	orderPoll     time.Duration // wait between checks of the market buy status
	orderRetries  int
}

func New(cfg *config.Config, store CandleStore, ex Exchange) *Bot {
	return &Bot{
		cfg:           cfg,
		store:         store,
		ex:            ex,
		now:           time.Now,
		candlePoll:    time.Second,
		candleRetries: 60,
		orderPoll:     500 * time.Millisecond,
		orderRetries:  10,
	}
}

func (b *Bot) Run(ctx context.Context) error {
	log.Println("Starting trading bot...")

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
			done, err := b.processCandle(ctx, s, expected)
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
	case "1d":
		return 24 * time.Hour, nil
	}
	return 0, fmt.Errorf("unsupported timeframe: %s", tf)
}

// processCandle handles the candle that started at expected. done is false
// only while that candle is not available yet: once it is, the candle is never
// processed again, so a failed step can not lead to a second buy.
func (b *Bot) processCandle(ctx context.Context, s config.StrategyConfig, expected time.Time) (done bool, err error) {
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

	if err := b.store.SaveCandles(ctx, []db.Candle{last}); err != nil {
		b.logf(s, "Failed to save candle to DB: %v", err)
	}

	signal, err := Signal(closed, s)
	if err != nil {
		return true, fmt.Errorf("signal: %w", err)
	}

	// No ctx here on purpose: a shutdown must not interrupt an entry between
	// the market buy and its stop loss.
	return true, b.trade(s, signal, last.Close)
}

// trade mirrors the backtest: at most one position, entered on the candle
// after any candle where the signal holds while no position is open.
func (b *Bot) trade(s config.StrategyConfig, signal bool, lastClose float64) error {
	inst, err := b.ex.GetInstrument(s.Symbol)
	if err != nil {
		return fmt.Errorf("instrument: %w", err)
	}

	// After a market buy fills there are no open orders left, so the
	// position is the base coin balance itself.
	held, err := b.ex.GetCoinBalance(inst.BaseCoin)
	if err != nil {
		return fmt.Errorf("%s balance: %w", inst.BaseCoin, err)
	}
	if hasPosition(inst, held) {
		b.logf(s, "Position open (%s %s), waiting for TP/SL", inst.FormatBaseQty(held), inst.BaseCoin)
		if exits, err := b.botStopOrders(s); err == nil && len(exits) == 0 {
			b.logf(s, "WARNING: position has no TP/SL orders of the bot, close it or add them by hand")
		}
		return nil
	}

	// The position is gone, so TP or SL has fired. The other one is still
	// waiting and would sell the next position at a stale price.
	if err := b.cancelStaleExits(s); err != nil {
		return fmt.Errorf("cancel stale exit orders: %w", err)
	}

	if !signal {
		b.logf(s, "No signal")
		return nil
	}

	return b.enter(s, inst, lastClose)
}

func hasPosition(inst exchange.Instrument, held float64) bool {
	qty := inst.FloorBaseQty(held)
	return qty > 0 && qty >= inst.MinOrderQty
}

// botStopOrders lists untriggered conditional orders placed by the bot.
func (b *Bot) botStopOrders(s config.StrategyConfig) ([]exchange.Order, error) {
	orders, err := b.ex.GetOpenStopOrders(s.Symbol)
	if err != nil {
		return nil, err
	}
	var own []exchange.Order
	for _, o := range orders {
		if strings.HasPrefix(o.OrderLinkID, orderLinkPrefix) {
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

// enter buys with deposit_percent of the quote balance, then protects the
// position with two conditional market sells. Spot market orders can not
// carry takeProfit/stopLoss on Bybit v5 (only Limit orders can), and both
// exits use orderFilter=StopOrder because a tpslOrder locks the coins, so a
// second one for the same coins would be rejected.
func (b *Bot) enter(s config.StrategyConfig, inst exchange.Instrument, lastClose float64) error {
	decimals := *s.Decimals

	// Refuse settings that can not work before any money moves.
	if _, sl := TpSlPrices(lastClose, s.TakeProfit, s.StopLoss, decimals); sl <= 0 {
		return fmt.Errorf("stop_loss %v with decimals %d gives a non-positive price for %v", s.StopLoss, decimals, lastClose)
	}

	quoteBalance, err := b.ex.GetCoinBalance(inst.QuoteCoin)
	if err != nil {
		return fmt.Errorf("%s balance: %w", inst.QuoteCoin, err)
	}
	amount := inst.FloorQuoteQty(quoteBalance * s.DepositPercent / 100)
	if amount < inst.MinOrderAmt || amount <= 0 {
		return fmt.Errorf("order amount %s %s is below the minimum %v", inst.FormatQuoteQty(amount), inst.QuoteCoin, inst.MinOrderAmt)
	}

	linkID := fmt.Sprintf("%s%s-%d", orderLinkPrefix, s.Symbol, b.now().UnixMilli())

	b.logf(s, "Signal: market buy for %s %s", inst.FormatQuoteQty(amount), inst.QuoteCoin)
	orderID, err := b.ex.PlaceOrder(exchange.MarketBuyByQuote(s.Symbol, inst.FormatQuoteQty(amount), linkID+"-buy"))
	if err != nil {
		return fmt.Errorf("market buy: %w", err)
	}

	// The backtest enters at the next candle open, which is the market price
	// right now; the actual average fill price is the closest equivalent.
	entry := lastClose
	order, err := b.waitClosed(s.Symbol, orderID)
	switch {
	case err != nil:
		b.logf(s, "WARNING: status of buy %s unknown (%v), using last close %v as entry price", orderID, err, lastClose)
	case order.CumExecQty <= 0:
		return fmt.Errorf("market buy %s not filled: %s", orderID, order.Status)
	default:
		entry = order.AvgPrice
	}

	// Sell what is actually held: the buy fee is taken in the base coin.
	held, err := b.ex.GetCoinBalance(inst.BaseCoin)
	if err != nil {
		return fmt.Errorf("UNPROTECTED POSITION: %s balance after buy: %w", inst.BaseCoin, err)
	}
	if !hasPosition(inst, held) {
		return fmt.Errorf("no %s to protect after buy %s (balance %v)", inst.BaseCoin, orderID, held)
	}
	qty := inst.FormatBaseQty(held)

	tp, sl := TpSlPrices(entry, s.TakeProfit, s.StopLoss, decimals)
	tpPrice, slPrice := inst.FormatPrice(tp), inst.FormatPrice(sl)

	// Stop loss first: if the take profit fails, the loss is still capped.
	if _, err := b.ex.PlaceOrder(exchange.StopMarketSell(s.Symbol, qty, slPrice, linkID+"-sl")); err != nil {
		return fmt.Errorf("UNPROTECTED POSITION: stop loss at %s: %w", slPrice, err)
	}
	if _, err := b.ex.PlaceOrder(exchange.StopMarketSell(s.Symbol, qty, tpPrice, linkID+"-tp")); err != nil {
		return fmt.Errorf("take profit at %s: %w", tpPrice, err)
	}

	b.logf(s, "Entered %s %s at %v: TP %s, SL %s", qty, inst.BaseCoin, entry, tpPrice, slPrice)
	return nil
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
	log.Printf("[%s %s] "+format, append([]any{s.Symbol, s.Timeframe}, args...)...)
}
