package analyzer

import (
	"config"
	"fmt"
	"indicator"
	"runtime"
	"source"
	"time"
)

type Quotes struct {
	Timestamps []time.Time
	Lows       []float64
	Opens      []float64
	Closes     []float64
	Highs      []float64
	Volumes    []float64
}

type TpSlParam struct {
	tp float64
	sl float64
}

type TpSlClose struct {
	next []int
	flag []float64
}

type Coef float64

type Analyzer struct {
	cfg    config.AnalyzerConfig
	quotes Quotes
}

type IndicatorParams struct {
	Type   indicator.Type
	Source source.Type
	Coef   Coef
}

type IndicatorsCompare struct {
	CurrentParams IndicatorParams
	NextParams    IndicatorParams
}

func New(cfg config.AnalyzerConfig, quotes Quotes) *Analyzer {
	return &Analyzer{
		cfg:    cfg,
		quotes: quotes,
	}
}

var (
	TpSlCloses           = make(map[TpSlParam]TpSlClose)
	IndicatorPrices      = make(map[IndicatorParams][]float64)
	IndicatorParamGroups = make(map[int][]IndicatorParams)
	IndicatorsCompares   = make(map[IndicatorsCompare][]int)
)

func (a *Analyzer) Length() int {
	return len(a.quotes.Timestamps)
}

func (a *Analyzer) Run() {
	runtime.GOMAXPROCS(a.cfg.Threads)

	a.fillIndicatorParamGroups()
	a.fillTakeprofitStoplossParams()
	a.fillIndicatorsCompares()

	fmt.Printf("IndicatorsCompares len:%d\n", len(IndicatorsCompares))

	//values := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 8, 4, 6, 9}

	//pr(values)
	//pr(indicator.calculateSma(values, 4))
	//pr(indicator.calculateEma(values, 1.5))
	//pr(indicator.calculateDema(values, 1.5))
	//pr(indicator.calculateTema(values, 1.5))
	//pr(indicator.calculateTemaZero(values, 1.5))

	//fmt.Println(s.quotes.Closes)
	//fmt.Println(calculateSma(3, s.quotes.Closes))

	//fmt.Println(tpCnt, slCnt, len(s.quotes.Closes))
}

func (a *Analyzer) fillIndicatorParamGroups() {
	for g, indicatorGroup := range a.cfg.Indicators {
		IndicatorParamGroups[g] = make([]IndicatorParams, 0)

		for _, ind := range indicatorGroup {
			for _, src := range ind.Sources {
				prices := a.getPricesBySource(src)

				coefCfg := ind.Coefs
				for coef := coefCfg.Start; coef <= coefCfg.End; coef += coefCfg.Step {
					params := IndicatorParams{
						Type:   ind.Type,
						Source: src,
						Coef:   Coef(coef),
					}
					if _, ok := IndicatorPrices[params]; !ok {
						IndicatorPrices[params] = ind.Type.CalculateIndicatorPrices(prices, coef)
					}
					IndicatorParamGroups[g] = append(IndicatorParamGroups[g], params)
				}
			}
		}
	}
}

func (a *Analyzer) fillTakeprofitStoplossParams() {
	ln := len(a.quotes.Opens)

	for tp := a.cfg.Takeprofit.Start; tp <= a.cfg.Takeprofit.End; tp += a.cfg.Takeprofit.Step {
		for sl := a.cfg.Stoploss.Start; sl <= a.cfg.Stoploss.End; sl += a.cfg.Stoploss.Step {
			param := TpSlParam{
				tp: tp,
				sl: sl,
			}
			if !a.hasEnoughCloses(param) {
				break
			}

			TpSlCloses[param] = a.calculateClosings(param, ln)
		}
	}
}

func (a *Analyzer) fillIndicatorsCompares() {
	for i := 0; i < len(IndicatorParamGroups)-1; i += 1 {
		currentParams := IndicatorParamGroups[i]

		for _, currentParam := range currentParams {
			for _, nextParam := range IndicatorParamGroups[i+1] {
				compareParams := IndicatorsCompare{
					CurrentParams: currentParam,
					NextParams:    nextParam,
				}
				indexes := a.compareIndicators(compareParams)

				if len(indexes) >= a.cfg.MinSignals {
					IndicatorsCompares[compareParams] = indexes
				}
			}
		}
	}
}

func (a *Analyzer) getPricesBySource(t source.Type) []float64 {
	switch t {
	case source.L:
		return a.quotes.Lows
	case source.O:
		return a.quotes.Opens
	case source.C:
		return a.quotes.Closes
	case source.H:
		return a.quotes.Highs
	case source.V:
		return a.quotes.Volumes
	}

	return nil
}

func (a *Analyzer) hasEnoughCloses(param TpSlParam) bool {
	openedPrice := 0.0
	closes := 0

	for i, v := range a.quotes.Opens {
		if openedPrice == 0.0 {
			openedPrice = v
		}

		if a.quotes.Highs[i] >= openedPrice+param.tp {
			closes++ // for long positions
			if closes >= a.cfg.MinCloses {
				return true
			}
			openedPrice = 0.0
		} else if a.quotes.Lows[i] <= openedPrice-param.sl {
			openedPrice = 0.0
		}
	}

	return false
}

func (a *Analyzer) calculateClosings(p TpSlParam, ln int) TpSlClose {
	c := TpSlClose{}

	for i, openedPrice := range a.quotes.Opens {
		for k := i; k < ln; k++ {
			if a.quotes.Highs[k] >= openedPrice+p.tp {
				c.next = append(c.next, k)
				c.flag = append(c.flag, 1.0)
				break
			} else if a.quotes.Lows[k] <= openedPrice-p.sl {
				c.next = append(c.next, k)
				c.flag = append(c.flag, -1.0)
				break
			}
		}
	}

	return c
}

func (a *Analyzer) compareIndicators(p IndicatorsCompare) []int {
	var (
		currentPrices = IndicatorPrices[p.CurrentParams]
		nextPrices    = IndicatorPrices[p.NextParams]
		ln            = len(currentPrices)
		indexes       = make([]int, 0)
	)

	for i := 0; i < ln; i++ {
		if nextPrices[i] > currentPrices[i] {
			indexes = append(indexes, i)
		}
	}

	return indexes
}

func pr(data []float64) {
	for _, v := range data {
		fmt.Printf("%6.2f ", v)
	}
	fmt.Printf("\n")
}
