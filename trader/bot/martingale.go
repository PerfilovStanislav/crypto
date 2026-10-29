package bot

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"trader/config"
)

// seriesState is the martingale state of one strategy. It is kept in state_file,
// so a restart does not forget the losses of the current series.
type seriesState struct {
	Base   float64 `json:"base"`   // USDT stake of the first trade of the series
	Loss   float64 `json:"loss"`   // USDT lost in the series so far
	Losses int     `json:"losses"` // stop losses in a row
	// Stopped says why the strategy no longer trades; the user resumes it by
	// removing its key (e.g. "SOLUSDT-L") from state_file.
	Stopped string    `json:"stopped,omitempty"`
	Open    *openDeal `json:"open,omitempty"`
}

// openDeal is what settle needs once the position is gone.
type openDeal struct {
	Opened  time.Time `json:"opened"` // the exit that closed the deal is later
	Entry   float64   `json:"entry"`
	Qty     float64   `json:"qty"` // base coin protected by TP/SL
	SLPrice float64   `json:"sl_price"`
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
	// The spot version keyed series by symbol and traded only longs; its open
	// deal was a spot trade, which the futures bot does not manage.
	for key, s := range st.all {
		if strings.HasSuffix(key, "-L") || strings.HasSuffix(key, "-S") {
			continue
		}
		if _, ok := st.all[key+"-L"]; !ok {
			s.Open = nil
			st.all[key+"-L"] = s
		}
		delete(st.all, key)
	}
	return st, nil
}

func (st *stateStore) get(key string) seriesState {
	st.mu.Lock()
	defer st.mu.Unlock()
	if s := st.all[key]; s != nil {
		return *s
	}
	return seriesState{}
}

// update changes the series of a strategy key and saves the whole state.
func (st *stateStore) update(key string, change func(*seriesState)) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	s := st.all[key]
	if s == nil {
		s = &seriesState{}
		st.all[key] = s
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
// order history: the position's TP or SL is the newest filled order of the
// strategy's side since the entry.
func (b *Bot) settle(s config.StrategyConfig) error {
	open := b.state.get(s.Key()).Open
	if open == nil {
		return nil
	}
	orders, err := b.ex.GetFilledOrders(s.Symbol)
	if err != nil {
		return err
	}
	var exit string
	for _, o := range orders { // newest first
		if o.PositionIdx == positionIdx(s) && !o.UpdatedTime.Before(open.Opened) &&
			(o.StopOrderType == "StopLoss" || o.StopOrderType == "TakeProfit") {
			exit = o.StopOrderType
			break
		}
	}

	var msg string
	err = b.state.update(s.Key(), func(st *seriesState) {
		st.Open = nil
		switch exit {
		case "StopLoss":
			// shortcut: комиссии и проскальзывание не учтены; точнее — по факту исполнения стоп-ордера.
			st.Loss += open.Qty * math.Abs(open.SLPrice-open.Entry)
			st.Losses++
			msg = fmt.Sprintf("🔴 %s: стоп-лосс %d подряд, убыток серии %.2f USDT", title(s), st.Losses, st.Loss)
			if s.MaxLosses > 0 && st.Losses >= s.MaxLosses {
				st.Stopped = fmt.Sprintf("%d stop losses in a row", st.Losses)
				msg += fmt.Sprintf("\n⛔ стратегия остановлена. Чтобы продолжить, удалите %s из %s", s.Key(), b.state.path)
			}
		case "TakeProfit":
			b.logf(s, "Take profit, martingale series closed")
			st.Loss, st.Losses = 0, 0
		default:
			st.Loss, st.Losses = 0, 0
			msg = fmt.Sprintf("⚠️ %s: не понять, чем закрылась сделка (в истории ордеров нет её TP/SL), серия мартингейла сброшена", title(s))
		}
	})
	if msg != "" {
		b.logf(s, "%s", msg)
		b.notifyf(s, "%s", msg)
	}
	return err
}

// martingaleStake is the USDT order value: the base stake while no stop loss
// is to be won back, then base + martingale * series loss / TP return, so the
// next take profit brings the usual profit plus the loss times martingale.
// ok is false when the balance can not cover it: the strategy is stopped.
func (b *Bot) martingaleStake(s config.StrategyConfig, quoteBalance, lastClose float64) (stake, base float64, ok bool, err error) {
	cur := b.state.get(s.Key())
	if cur.Losses == 0 {
		base = quoteBalance * s.DepositPercent / 100
		return base, base, true, nil
	}
	tp, _ := TpSlPrices(lastClose, s.TakeProfit, s.StopLoss, *s.Decimals, s.Short())
	stake = cur.Base + s.Martingale*cur.Loss/(math.Abs(tp-lastClose)/lastClose)
	if stake <= quoteBalance {
		return stake, cur.Base, true, nil
	}

	err = b.state.update(s.Key(), func(st *seriesState) {
		st.Stopped = fmt.Sprintf("stake %.2f USDT is above the balance %.2f USDT", stake, quoteBalance)
	})
	b.notifyf(s, "⛔ %s: на ставку мартингейла нужно %.2f USDT, а на балансе %.2f. Стратегия остановлена; чтобы продолжить, удалите %s из %s",
		title(s), stake, quoteBalance, s.Key(), b.state.path)
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
