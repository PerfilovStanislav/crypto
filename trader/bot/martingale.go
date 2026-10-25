package bot

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"trader/config"
)

// seriesState is the martingale state of one strategy. It is kept in state_file,
// so a restart does not forget the losses of the current series.
type seriesState struct {
	Base   float64 `json:"base"`   // USDT stake of the first trade of the series
	Loss   float64 `json:"loss"`   // USDT lost in the series so far
	Losses int     `json:"losses"` // stop losses in a row
	// Stopped says why the strategy no longer trades; the user resumes it by
	// removing the symbol from state_file.
	Stopped string    `json:"stopped,omitempty"`
	Open    *openDeal `json:"open,omitempty"`
}

// openDeal is what settle needs once the position is gone.
type openDeal struct {
	LinkID  string  `json:"link_id"`
	Spent   float64 `json:"spent"` // USDT spent on the buy
	Qty     float64 `json:"qty"`   // base coin protected by the exits
	SLPrice float64 `json:"sl_price"`
}

type stateStore struct {
	path string
	mu   sync.Mutex
	all  map[string]*seriesState
}

// loadState reads state_file; a missing file is an empty state.
func loadState(path string) (*stateStore, error) {
	st := &stateStore{path: path, all: map[string]*seriesState{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &st.all); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return st, nil
}

func (st *stateStore) get(symbol string) seriesState {
	st.mu.Lock()
	defer st.mu.Unlock()
	if s := st.all[symbol]; s != nil {
		return *s
	}
	return seriesState{}
}

// update changes the series of symbol and saves the whole state.
func (st *stateStore) update(symbol string, change func(*seriesState)) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	s := st.all[symbol]
	if s == nil {
		s = &seriesState{}
		st.all[symbol] = s
	}
	change(s)

	data, err := json.MarshalIndent(st.all, "", "  ")
	if err != nil {
		return err
	}
	// Rename replaces the file at once: a crash never leaves half of it.
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, st.path)
}

// settle books the deal that has just closed. Which exit fired shows in the
// bot's exit order that is still waiting: a TP left means the SL fired.
func (b *Bot) settle(s config.StrategyConfig) error {
	open := b.state.get(s.Symbol).Open
	if open == nil {
		return nil
	}
	orders, err := b.botStopOrders(s)
	if err != nil {
		return err
	}
	var tpLeft, slLeft bool
	for _, o := range orders {
		tpLeft = tpLeft || o.OrderLinkID == open.LinkID+"-tp"
		slLeft = slLeft || o.OrderLinkID == open.LinkID+"-sl"
	}

	var msg string
	err = b.state.update(s.Symbol, func(st *seriesState) {
		st.Open = nil
		switch {
		case tpLeft && !slLeft:
			// shortcut: комиссия продажи и проскальзывание не учтены; точнее — по факту исполнения стоп-ордера.
			st.Loss += open.Spent - open.Qty*open.SLPrice
			st.Losses++
			msg = fmt.Sprintf("🔴 %s %s: стоп-лосс %d подряд, убыток серии %.2f USDT", s.Symbol, s.Timeframe, st.Losses, st.Loss)
			if s.MaxLosses > 0 && st.Losses >= s.MaxLosses {
				st.Stopped = fmt.Sprintf("%d stop losses in a row", st.Losses)
				msg += fmt.Sprintf("\n⛔ стратегия остановлена. Чтобы продолжить, удалите %s из %s", s.Symbol, b.state.path)
			}
		case slLeft && !tpLeft:
			b.logf(s, "Take profit, martingale series closed")
			st.Loss, st.Losses = 0, 0
		default:
			st.Loss, st.Losses = 0, 0
			msg = fmt.Sprintf("⚠️ %s %s: не понять, чем закрылась сделка (у бота нет её стоп-ордеров), серия мартингейла сброшена", s.Symbol, s.Timeframe)
		}
	})
	if msg != "" {
		b.logf(s, "%s", msg)
		b.notifyf(s, "%s", msg)
	}
	return err
}

// martingaleStake is the USDT to buy for: the base stake while no stop loss
// is to be won back, then base + martingale * series loss / TP return, so the
// next take profit brings the usual profit plus the loss times martingale.
// ok is false when the balance can not cover it: the strategy is stopped.
func (b *Bot) martingaleStake(s config.StrategyConfig, quoteBalance, lastClose float64) (stake, base float64, ok bool, err error) {
	cur := b.state.get(s.Symbol)
	if cur.Losses == 0 {
		base = quoteBalance * s.DepositPercent / 100
		return base, base, true, nil
	}
	tp, _ := TpSlPrices(lastClose, s.TakeProfit, s.StopLoss, *s.Decimals)
	stake = cur.Base + s.Martingale*cur.Loss/((tp-lastClose)/lastClose)
	if stake <= quoteBalance {
		return stake, cur.Base, true, nil
	}

	err = b.state.update(s.Symbol, func(st *seriesState) {
		st.Stopped = fmt.Sprintf("stake %.2f USDT is above the balance %.2f USDT", stake, quoteBalance)
	})
	b.notifyf(s, "⛔ %s %s: на ставку мартингейла нужно %.2f USDT, а на балансе %.2f. Стратегия остановлена; чтобы продолжить, удалите %s из %s",
		s.Symbol, s.Timeframe, stake, quoteBalance, s.Symbol, b.state.path)
	return 0, 0, false, err
}

func usesMartingale(cfg *config.Config) bool {
	for _, s := range cfg.Strategies {
		if s.Martingale > 0 {
			return true
		}
	}
	return false
}
