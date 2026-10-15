package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExampleConfigIsValid(t *testing.T) {
	cfg, err := Load("../config.yaml.example")
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.Strategies[0]
	if s.Decimals == nil || *s.Decimals != 2 || s.TakeProfit != 2000 || s.StopLoss != 1500 {
		t.Errorf("strategy = %+v", s)
	}
}

func TestValidate(t *testing.T) {
	const valid = `
strategies:
  - symbol: SOLUSDT
    timeframe: 4h
    deposit_percent: 5
    decimals: 2
    take_profit: 2000
    stop_loss: 1500
    indicator1: {type: sma, source: C, coef: 5}
    indicator2: {type: ema, source: C, coef: 0.5}
`
	cases := []struct {
		name, from, to, wantErr string
	}{
		{"valid", "", "", ""},
		{"decimals missing", "    decimals: 2\n", "", "decimals is required"},
		{"decimals zero is explicit", "decimals: 2", "decimals: 0", ""},
		{"ema coef as period", "coef: 0.5", "coef: 10", "multiplier"},
		{"sma fractional period", "coef: 5}", "coef: 5.5}", "period"},
		{"unknown type", "type: ema", "type: wma", "unknown type"},
		{"no stop loss", "stop_loss: 1500", "stop_loss: 0", "stop_loss"},
		{"deposit over 100", "deposit_percent: 5", "deposit_percent: 150", "deposit_percent"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			yml := valid
			if c.from != "" {
				if !strings.Contains(yml, c.from) {
					t.Fatalf("setup: %q not found", c.from)
				}
				yml = strings.Replace(yml, c.from, c.to, 1)
			}
			_, err := Load(writeTemp(t, yml))
			if c.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
				t.Fatalf("err = %v, want %q", err, c.wantErr)
			}
		})
	}

	dup := valid + strings.TrimPrefix(valid, "\nstrategies:\n")
	if _, err := Load(writeTemp(t, dup)); err == nil || !strings.Contains(err.Error(), "one strategy per symbol") {
		t.Errorf("duplicate symbol: err = %v", err)
	}
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
