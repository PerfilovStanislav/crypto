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

type TpSlParams struct {
	tp   float64
	sl   float64
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

var IndicatorPrices = make(map[IndicatorParams][]float64)

var IndicatorParamGroups = make(map[int][]IndicatorParams)

var IndicatorsCompares = make(map[IndicatorsCompare][]int)

func (a *Analyzer) Length() int {
	return len(a.quotes.Timestamps)
}

var m runtime.MemStats

func (a *Analyzer) Run() {
	start := time.Now()

	runtime.ReadMemStats(&m)
	runtime.GOMAXPROCS(a.cfg.Threads)

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

	for tp := 5.0; tp < 30.0; tp += 1.0 {
		for sl := 5.0; sl < 30.0; sl += 1.0 {
			if !a.hasEnoughCloses(tp, sl) {
				break
			}

			// TODO не сохряняем!!
			a.prepareClosings(TpSlParams{
				tp:   tp,
				sl:   sl,
				next: make([]int, 0, len(a.quotes.Opens)),
				flag: make([]float64, 0, len(a.quotes.Opens)),
			})
		}
	}

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
	fmt.Printf("IndicatorsCompares len:%d\n", len(IndicatorsCompares))

	fmt.Printf("Execution time: %s\n", time.Since(start))
	fmt.Printf("Alloc = %d MiB\n", bToMb(m.Alloc))
	fmt.Printf("TotalAlloc = %d MiB\n", bToMb(m.TotalAlloc))
	fmt.Printf("Sys = %d MiB\n", bToMb(m.Sys))
	fmt.Printf("NumGC = %d\n", m.NumGC)

	//fmt.Println(len(IndicatorsCompares))

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

func bToMb(b uint64) uint64 {
	return b / 1024 / 1024
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

func (a *Analyzer) hasEnoughCloses(tp, sl float64) bool {
	openedPrice := 0.0
	closes := 0

	for i, v := range a.quotes.Opens {
		if openedPrice > 0 {
			if a.quotes.Highs[i] >= openedPrice+tp {
				closes++ // for long positions
				if closes >= a.cfg.MinCloses {
					return true
				}
				openedPrice = 0.0
			} else if a.quotes.Lows[i] <= openedPrice-sl {
				openedPrice = 0.0
			}
		} else {
			openedPrice = v
		}
	}

	return false
}

func (a *Analyzer) prepareClosings(p TpSlParams) {
	ln := len(a.quotes.Opens)

	for i, openedPrice := range a.quotes.Opens {
		for k := i + 1; k < ln; k++ {
			if a.quotes.Highs[k] >= openedPrice+p.tp {
				p.next = append(p.next, k)
				p.flag = append(p.flag, 1.0)
				break
			} else if a.quotes.Lows[k] <= openedPrice-p.sl {
				p.next = append(p.next, k)
				p.flag = append(p.flag, -1.0)
				break
			}
		}
	}

	return
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
