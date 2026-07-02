package analyzer

import (
	"config"
	"fmt"
	"indicator"
	"runtime"
	"source"
	"time"

	"github.com/fatih/color"
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
	indexes []int
	coefs   []float64
}

type Coef float64

type Analyzer struct {
	cfg     config.AnalyzerConfig
	quotes  Quotes
	ln      int
	Results chan TaskResult
}

type IndicatorParams struct {
	Type   indicator.Type
	Source source.Type
	Coef   Coef
}

type IndicatorsCompare struct {
	Indicator1Params IndicatorParams
	Indicator2Params IndicatorParams
}

type Task struct {
	IndicatorsCompare IndicatorsCompare
	TpSlParam         TpSlParam
}

type TaskResult struct {
	Task Task
	Coef float64
}

var (
	TpSlCloses           = make(map[TpSlParam]TpSlClose)
	IndicatorPrices      = make(map[IndicatorParams][]float64)
	IndicatorParamGroups = make(map[int][]IndicatorParams)
	IndicatorsCompares   = make(map[IndicatorsCompare][]int)
)

func New(cfg config.AnalyzerConfig, quotes Quotes) *Analyzer {
	return &Analyzer{
		cfg:     cfg,
		quotes:  quotes,
		ln:      len(quotes.Opens),
		Results: make(chan TaskResult, cfg.Threads),
	}
}

func (a *Analyzer) Run() {
	runtime.GOMAXPROCS(a.cfg.Threads)

	a.fillIndicatorParamGroups()
	a.fillTakeprofitStoplossParams()
	a.fillIndicatorsCompares()

	resultDoneSignal := a.resultChannelHandler()

	fmt.Printf("IndicatorsCompares len:%d\n", len(IndicatorsCompares))

	taskChannel := make(chan Task)
	ready := make(chan struct{}, a.cfg.Threads)

	for i := 0; i < a.cfg.Threads; i++ {
		go func(taskChannel <-chan Task, ready chan<- struct{}) {
			for task := range taskChannel {
				a.testTask(task)
			}
			ready <- struct{}{}
		}(taskChannel, ready)
	}

	for indicatorsCompare, _ := range IndicatorsCompares {
		for tpSlParam, _ := range TpSlCloses {
			taskChannel <- Task{indicatorsCompare, tpSlParam}
		}
	}
	close(taskChannel)

	for i := 0; i < a.cfg.Threads; i++ {
		<-ready
	}
	close(ready)

	close(a.Results)
	<-resultDoneSignal

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

func (a *Analyzer) testTask(t Task) {
	// здесь берём один уровень. Если надо будет сравнивать 3 индикатора, то придётся позаморачиваться

	var (
		result       = 1.0
		signals      = IndicatorsCompares[t.IndicatorsCompare]
		coefs        = TpSlCloses[t.TpSlParam].coefs
		indexes      = TpSlCloses[t.TpSlParam].indexes
		closingIndex = -1
	)

	for i := 0; i < len(signals); i++ {
		openingSignalIndex := signals[i]
		if openingSignalIndex < closingIndex {
			continue
		}
		result *= coefs[openingSignalIndex+1] // открытие происходит на следующей свече
		closingIndex = indexes[openingSignalIndex+1]
	}

	if result > 1.0 {
		a.Results <- TaskResult{
			Task: t,
			Coef: result,
		}
	}
}

func (a *Analyzer) resultChannelHandler() <-chan struct{} {
	maxCoef := 0.0

	done := make(chan struct{})

	go func() {
		defer close(done)

		for r := range a.Results {
			if r.Coef > maxCoef {
				maxCoef = r.Coef
				fmt.Println(r)
			}
		}
	}()

	return done
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
	for tp := a.cfg.Takeprofit.Start; tp <= a.cfg.Takeprofit.End; tp += a.cfg.Takeprofit.Step {
		for sl := a.cfg.Stoploss.Start; sl <= a.cfg.Stoploss.End; sl += a.cfg.Stoploss.Step {
			param := TpSlParam{
				tp: tp,
				sl: sl,
			}
			if !a.hasEnoughCloses(param) {
				break
			}

			TpSlCloses[param] = a.calculateClosings(param)
		}
	}
}

func (a *Analyzer) fillIndicatorsCompares() {
	for i := 0; i < len(IndicatorParamGroups)-1; i += 1 {
		currentParams := IndicatorParamGroups[i]

		for _, currentParam := range currentParams {
			for _, nextParam := range IndicatorParamGroups[i+1] {
				compareParams := IndicatorsCompare{
					Indicator1Params: currentParam,
					Indicator2Params: nextParam,
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
	case source.LO:
		return AverageSlices(a.quotes.Lows, a.quotes.Opens)
	case source.LC:
		return AverageSlices(a.quotes.Lows, a.quotes.Closes)
	case source.LH:
		return AverageSlices(a.quotes.Lows, a.quotes.Highs)
	case source.OC:
		return AverageSlices(a.quotes.Opens, a.quotes.Closes)
	case source.OH:
		return AverageSlices(a.quotes.Opens, a.quotes.Highs)
	case source.CH:
		return AverageSlices(a.quotes.Closes, a.quotes.Highs)
	case source.LOC:
		return AverageSlices(a.quotes.Lows, a.quotes.Opens, a.quotes.Closes)
	case source.LOH:
		return AverageSlices(a.quotes.Lows, a.quotes.Opens, a.quotes.Highs)
	case source.LCH:
		return AverageSlices(a.quotes.Lows, a.quotes.Closes, a.quotes.Highs)
	case source.OCH:
		return AverageSlices(a.quotes.Opens, a.quotes.Closes, a.quotes.Highs)
	}

	return nil
}

func AverageSlices(slices ...[]float64) []float64 {
	length := len(slices[0])
	result := make([]float64, length)

	for _, slice := range slices {
		for i, v := range slice {
			result[i] += v
		}
	}

	divisor := float64(len(slices))
	for i := range result {
		result[i] /= divisor
	}

	return result
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

func (a *Analyzer) calculateClosings(p TpSlParam) TpSlClose {
	c := TpSlClose{
		indexes: make([]int, a.ln),
		coefs:   make([]float64, a.ln),
	}
	commsision := (1 - a.cfg.Commission) * (1 - a.cfg.Commission)

level2:
	for i, openedPrice := range a.quotes.Opens {
		for k := i; k < a.ln; k++ {
			if a.quotes.Highs[k] >= openedPrice+p.tp {
				coef := commsision * (openedPrice + p.tp) / openedPrice
				c.indexes[i] = k
				c.coefs[i] = coef
				continue level2
			} else if a.quotes.Lows[k] <= openedPrice-p.sl {
				coef := commsision * (openedPrice - p.sl) / openedPrice
				c.indexes[i] = k
				c.coefs[i] = coef
				continue level2
			}
		}
		for k := i; k < a.ln; k++ {
			c.indexes[k] = a.ln
			c.coefs[k] = 1.0
		}
	}

	return c
}

func (a *Analyzer) compareIndicators(p IndicatorsCompare) []int {
	var (
		currentPrices = IndicatorPrices[p.Indicator1Params]
		nextPrices    = IndicatorPrices[p.Indicator2Params]
		indexes       = make([]int, 0)
	)

	for i := 0; i < a.ln-1; i++ { // don't check last candle
		if nextPrices[i] > currentPrices[i] {
			indexes = append(indexes, i)
		}
	}

	return indexes
}

func clr(text string, attrs ...color.Attribute) string {
	c := color.New(attrs...)
	c.EnableColor()
	return c.Sprintf("%s", text)
}

func spf(f string, a ...any) string {
	return fmt.Sprintf(f, a...)
}

func pr(data []float64) {
	for _, v := range data {
		fmt.Printf("%6.2f ", v)
	}
	fmt.Printf("\n")
}
