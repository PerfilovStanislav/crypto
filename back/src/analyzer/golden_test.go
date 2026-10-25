package analyzer

import (
	"config"
	"encoding/json"
	"indicator"
	"math"
	"math/rand"
	"os"
	"source"
	"testing"
	"time"
)

// TestGolden пишет фикстуры для сверки Rust-порта (back-rs/tests/golden.rs).
// Запуск из back/src:
//
//	GOLDEN_OUT=$PWD/../../back-rs/tests/fixtures/golden.json go test ./analyzer -run TestGolden
func TestGolden(t *testing.T) {
	out := os.Getenv("GOLDEN_OUT")
	if out == "" {
		t.Skip("GOLDEN_OUT is not set")
	}

	quotes := syntheticQuotes(600, 42)
	cfg := config.AnalyzerConfig{
		Threads:          4,
		Pair:             "TESTUSDT",
		Timeframe:        "4h",
		Commission:       0.001,
		MinCloses:        20,
		MinSignals:       50,
		MinRecentSignals: 2,
		RecentMonths:     1,
		MinTakeProfits:   3,
		Takeprofit:       config.RangeConfig{Start: 2.0, End: 6.0, Step: 1.0},
		Stoploss:         config.RangeConfig{Start: 3.0, End: 9.0, Step: 1.5},
		Indicators: [][]config.IndicatorConfig{
			{
				{Type: indicator.Sma, Coefs: config.RangeConfig{Start: 3, End: 7, Step: 1}, Sources: []source.Type{source.C, source.LO}},
				{Type: indicator.Ema, Coefs: config.RangeConfig{Start: 0.2, End: 0.6, Step: 0.1}, Sources: []source.Type{source.C, source.H}},
			},
			{
				{Type: indicator.Dema, Coefs: config.RangeConfig{Start: 0.2, End: 0.5, Step: 0.1}, Sources: []source.Type{source.C, source.OCH}},
				{Type: indicator.Tema, Coefs: config.RangeConfig{Start: 0.3, End: 0.6, Step: 0.1}, Sources: []source.Type{source.LOC}},
				{Type: indicator.TemaZero, Coefs: config.RangeConfig{Start: 0.2, End: 0.4, Step: 0.1}, Sources: []source.Type{source.C}},
				{Type: indicator.Sma, Coefs: config.RangeConfig{Start: 10, End: 14, Step: 1}, Sources: []source.Type{source.C}},
			},
		},
	}

	a := New(cfg, quotes)
	a.fillIndicatorParamGroups()
	a.fillTakeprofitStoplossParams()
	a.fillIndicatorsCompares()

	g := golden{
		Config:      goldenConfig(cfg),
		CutoffIndex: a.cutoffIndex,
		Quotes: goldenQuotes{
			Lows: quotes.Lows, Opens: quotes.Opens, Closes: quotes.Closes, Highs: quotes.Highs, Volumes: quotes.Volumes,
		},
	}
	for _, ts := range quotes.Timestamps {
		g.Quotes.Timestamps = append(g.Quotes.Timestamps, ts.Unix())
	}
	for params, values := range IndicatorPrices {
		g.Indicators = append(g.Indicators, goldenIndicator{Key: key(params), Values: values})
	}

	tpSl := make([]TpSlParam, 0, len(TpSlCloses))
	for p, c := range TpSlCloses {
		tpSl = append(tpSl, p)
		g.TpSl = append(g.TpSl, goldenTpSl{Tp: p.Tp, Sl: p.Sl, Indexes: c.Indexes, Coefs: c.Coefs, DrawDowns: c.DrawDowns})
	}

	for ic, signals := range IndicatorsCompares {
		ci := len(g.Compares)
		g.Compares = append(g.Compares, goldenCompare{Ind1: key(ic.Indicator1Params), Ind2: key(ic.Indicator2Params), Signals: signals})

		for ti, p := range tpSl {
			c := TpSlCloses[p]
			// Свежий анализатор: максимум 0, значит в канал попадёт любой результат, прошедший фильтры.
			b := &Analyzer{Cfg: cfg, ln: a.ln, Results: make(chan TaskResult, 1)}
			b.testTaskDirect(ic, signals, p, c.Indexes, c.Coefs, c.DrawDowns)
			select {
			case r := <-b.Results:
				g.Passed = append(g.Passed, goldenEval{
					Compare: ci, TpSl: ti,
					Coef: r.Coef, MaxDrawdown: r.MaxDrawdown, ProfitToDd: jsonFloat(r.ProfitToDd),
					ProfitToCandles: r.ProfitToCandles, Wins: r.Wins, Losses: r.Losses,
				})
			default:
			}
		}
	}
	t.Logf("indicators=%d tpsl=%d compares=%d passed=%d cutoff=%d",
		len(g.Indicators), len(g.TpSl), len(g.Compares), len(g.Passed), g.CutoffIndex)

	data, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// syntheticQuotes — детерминированное случайное блуждание с восходящим трендом,
// последняя свеча — текущая 4h-свеча, чтобы cutoffIndex попадал внутрь ряда.
func syntheticQuotes(n int, seed int64) source.Quotes {
	rnd := rand.New(rand.NewSource(seed))
	step := 4 * time.Hour
	start := time.Now().UTC().Truncate(step).Add(-time.Duration(n-1) * step)

	q := source.Quotes{}
	price := 100.0
	for i := 0; i < n; i++ {
		open := price
		closePrice := open * math.Exp(0.0015+0.012*rnd.NormFloat64())
		high := math.Max(open, closePrice) * (1 + 0.01*math.Abs(rnd.NormFloat64()))
		low := math.Min(open, closePrice) * (1 - 0.01*math.Abs(rnd.NormFloat64()))

		q.Timestamps = append(q.Timestamps, start.Add(time.Duration(i)*step))
		q.Opens = append(q.Opens, open)
		q.Closes = append(q.Closes, closePrice)
		q.Highs = append(q.Highs, high)
		q.Lows = append(q.Lows, low)
		q.Volumes = append(q.Volumes, 1000+500*rnd.Float64())
		price = closePrice
	}
	return q
}

type golden struct {
	Config      map[string]any    `json:"config"`
	CutoffIndex int               `json:"cutoffIndex"`
	Quotes      goldenQuotes      `json:"quotes"`
	Indicators  []goldenIndicator `json:"indicators"`
	TpSl        []goldenTpSl      `json:"tpSl"`
	Compares    []goldenCompare   `json:"compares"`
	Passed      []goldenEval      `json:"passed"`
}

type goldenQuotes struct {
	Timestamps []int64   `json:"timestamps"`
	Lows       []float64 `json:"lows"`
	Opens      []float64 `json:"opens"`
	Closes     []float64 `json:"closes"`
	Highs      []float64 `json:"highs"`
	Volumes    []float64 `json:"volumes"`
}

type goldenKey struct {
	Type   string  `json:"type"`
	Source string  `json:"source"`
	Coef   float64 `json:"coef"`
}

type goldenIndicator struct {
	Key    goldenKey `json:"key"`
	Values []float64 `json:"values"`
}

type goldenTpSl struct {
	Tp        float64   `json:"tp"`
	Sl        float64   `json:"sl"`
	Indexes   []int     `json:"indexes"`
	Coefs     []float64 `json:"coefs"`
	DrawDowns []float64 `json:"drawdowns"`
}

type goldenCompare struct {
	Ind1    goldenKey `json:"ind1"`
	Ind2    goldenKey `json:"ind2"`
	Signals []int     `json:"signals"`
}

type goldenEval struct {
	Compare         int     `json:"compare"`
	TpSl            int     `json:"tpSl"`
	Coef            float64 `json:"coef"`
	MaxDrawdown     float64 `json:"maxDrawdown"`
	ProfitToDd      any     `json:"profitToDd"`
	ProfitToCandles float64 `json:"profitToCandles"`
	Wins            int     `json:"wins"`
	Losses          int     `json:"losses"`
}

func key(p IndicatorParams) goldenKey {
	return goldenKey{Type: string(p.Type), Source: string(p.Source), Coef: float64(p.Coef)}
}

// jsonFloat: encoding/json не умеет ±Inf и NaN, такие значения пишем строкой.
func jsonFloat(v float64) any {
	switch {
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	case math.IsNaN(v):
		return "NaN"
	}
	return v
}

func goldenConfig(c config.AnalyzerConfig) map[string]any {
	rng := func(r config.RangeConfig) map[string]any {
		return map[string]any{"start": r.Start, "end": r.End, "step": r.Step}
	}
	var groups [][]map[string]any
	for _, group := range c.Indicators {
		var g []map[string]any
		for _, ind := range group {
			g = append(g, map[string]any{"type": ind.Type, "coefs": rng(ind.Coefs), "sources": ind.Sources})
		}
		groups = append(groups, g)
	}
	return map[string]any{
		"threads":          c.Threads,
		"pair":             c.Pair,
		"timeframe":        c.Timeframe,
		"commission":       c.Commission,
		"decimals":         c.Decimals,
		"minCloses":        c.MinCloses,
		"minSignals":       c.MinSignals,
		"minRecentSignals": c.MinRecentSignals,
		"recentMonths":     c.RecentMonths,
		"minTakeProfits":   c.MinTakeProfits,
		"takeprofit":       rng(c.Takeprofit),
		"stoploss":         rng(c.Stoploss),
		"indicators":       groups,
	}
}
