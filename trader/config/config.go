package config

import (
	"fmt"
	"math"
	"os"

	"gopkg.in/yaml.v3"
)

type BybitConfig struct {
	APIKey    string `yaml:"api_key"`
	APISecret string `yaml:"api_secret"`
	// BaseURL is optional; defaults to https://api.bybit.com.
	// Use https://api-testnet.bybit.com for the testnet.
	BaseURL string `yaml:"base_url"`
}

// TelegramConfig is optional: without a token the bot sends no notifications.
type TelegramConfig struct {
	Token  string `yaml:"token"`
	ChatID string `yaml:"chat_id"`
}

type IndicatorConfig struct {
	Type   string  `yaml:"type"`
	Source string  `yaml:"source"`
	Coef   float64 `yaml:"coef"`
}

type StrategyConfig struct {
	Symbol string `yaml:"symbol"`
	// Direction is "buy" (long) or "sell" (short), as analyzer.direction;
	// empty means "buy".
	Direction      string  `yaml:"direction"`
	Timeframe      string  `yaml:"timeframe"`
	DepositPercent float64 `yaml:"deposit_percent"`
	// TakeProfit and StopLoss are offsets from the entry price in units of
	// 10^-Decimals, exactly as in the analyzer config (SOLUSDT, decimals 2:
	// take_profit 2000 means entry + 20.00 USDT).
	TakeProfit float64 `yaml:"take_profit"`
	StopLoss   float64 `yaml:"stop_loss"`
	// Decimals is required: a missing value would silently turn 2000 into
	// +2000 USDT. Use 0 explicitly when offsets are already in price units.
	Decimals   *int            `yaml:"decimals"`
	Indicator1 IndicatorConfig `yaml:"indicator1"`
	Indicator2 IndicatorConfig `yaml:"indicator2"`
	// Martingale > 0: after a stop loss the next buy grows so that its take
	// profit also wins back the series loss times Martingale, as in the
	// analyzer; deposit_percent is then the first stake of a series.
	Martingale float64 `yaml:"martingale"`
	// MaxLosses stops a martingale strategy after this many stop losses in a
	// row; 0 means no limit.
	MaxLosses int `yaml:"max_losses"`
}

type Config struct {
	Bybit      BybitConfig      `yaml:"bybit"`
	Telegram   TelegramConfig   `yaml:"telegram"`
	Strategies []StrategyConfig `yaml:"strategies"`
	// StateFile keeps the martingale series between restarts.
	StateFile string `yaml:"state_file"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	for i := range cfg.Strategies {
		if cfg.Strategies[i].Direction == "" {
			cfg.Strategies[i].Direction = "buy"
		}
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.StateFile == "" {
		cfg.StateFile = "state.json"
	}

	return &cfg, nil
}

func (c *Config) Validate() error {
	if len(c.Strategies) == 0 {
		return fmt.Errorf("no strategies configured")
	}
	if (c.Telegram.Token == "") != (c.Telegram.ChatID == "") {
		return fmt.Errorf("telegram: set both token and chat_id, or neither")
	}

	// In hedge mode a strategy owns one side of the symbol's position (long or
	// short) and tells from its size whether it is in a trade, so two strategies
	// on one side would fight over it.
	seen := make(map[string]bool)
	for _, s := range c.Strategies {
		if s.Symbol == "" {
			return fmt.Errorf("strategy without symbol")
		}
		if s.Direction != "buy" && s.Direction != "sell" {
			return fmt.Errorf("strategy %s: direction must be buy or sell, got %q", s.Symbol, s.Direction)
		}
		if seen[s.Key()] {
			return fmt.Errorf("strategy %s %s: only one strategy per symbol and direction is supported", s.Symbol, s.Direction)
		}
		seen[s.Key()] = true

		if s.Decimals == nil {
			return fmt.Errorf("strategy %s: decimals is required (same value as in the analyzer config)", s.Symbol)
		}
		if *s.Decimals < 0 {
			return fmt.Errorf("strategy %s: decimals must be >= 0", s.Symbol)
		}
		if s.TakeProfit <= 0 || s.StopLoss <= 0 {
			return fmt.Errorf("strategy %s: take_profit and stop_loss must be > 0", s.Symbol)
		}
		if s.DepositPercent <= 0 || s.DepositPercent > 100 {
			return fmt.Errorf("strategy %s: deposit_percent must be in (0, 100]", s.Symbol)
		}
		if s.Martingale < 0 || s.MaxLosses < 0 {
			return fmt.Errorf("strategy %s: martingale and max_losses must be >= 0", s.Symbol)
		}
		if err := s.Indicator1.validate(); err != nil {
			return fmt.Errorf("strategy %s: indicator1: %w", s.Symbol, err)
		}
		if err := s.Indicator2.validate(); err != nil {
			return fmt.Errorf("strategy %s: indicator2: %w", s.Symbol, err)
		}
	}

	return nil
}

// Short reports a sell (short) strategy.
func (s StrategyConfig) Short() bool {
	return s.Direction == "sell"
}

// Key names the strategy in logs, order link ids and state_file: "SOLUSDT-L"
// for the long, "SOLUSDT-S" for the short.
func (s StrategyConfig) Key() string {
	if s.Short() {
		return s.Symbol + "-S"
	}
	return s.Symbol + "-L"
}

func (ic IndicatorConfig) validate() error {
	switch ic.Type {
	case "sma":
		if ic.Coef < 1 || ic.Coef != math.Trunc(ic.Coef) {
			return fmt.Errorf("sma coef is a period, must be an integer >= 1, got %v", ic.Coef)
		}
	case "ema", "dema", "tema", "temaZero":
		// prev*(1-coef) diverges once |1-coef| >= 1
		if ic.Coef <= 0 || ic.Coef >= 2 {
			return fmt.Errorf("%s coef is a multiplier, must be in (0, 2), got %v", ic.Type, ic.Coef)
		}
	default:
		return fmt.Errorf("unknown type %q", ic.Type)
	}
	return nil
}
