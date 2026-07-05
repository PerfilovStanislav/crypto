package analyzer

import (
	"config"
	"fmt"
	"indicator"
	"math"
	"runtime"
	"source"
	"sync"
	"sync/atomic"
)

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
	cfg         config.AnalyzerConfig
	quotes      source.Quotes
	ln          int
	Results     chan TaskResult
	maxCoefBits uint64
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

func New(cfg config.AnalyzerConfig, quotes source.Quotes) *Analyzer {
	return &Analyzer{
		cfg:     cfg,
		quotes:  quotes,
		ln:      len(quotes.Opens),
		Results: make(chan TaskResult, cfg.Threads),
	}
}

func (a *Analyzer) updateMaxCoef(val float64) bool {
	for {
		currentBits := atomic.LoadUint64(&a.maxCoefBits)
		currentVal := math.Float64frombits(currentBits)
		if val <= currentVal {
			return false
		}
		newBits := math.Float64bits(val)
		if atomic.CompareAndSwapUint64(&a.maxCoefBits, currentBits, newBits) {
			return true
		}
	}
}

func (a *Analyzer) Run() {
	runtime.GOMAXPROCS(a.cfg.Threads)

	a.fillIndicatorParamGroups()
	a.fillTakeprofitStoplossParams()
	a.fillIndicatorsCompares()

	resultDoneSignal := a.resultChannelHandler()

	fmt.Printf("IndicatorsCompares len:%d\n", len(IndicatorsCompares))

	type compareJob struct {
		ic      IndicatorsCompare
		signals []int
	}

	jobs := make([]compareJob, 0, len(IndicatorsCompares))
	for ic, signals := range IndicatorsCompares {
		jobs = append(jobs, compareJob{ic, signals})
	}

	numJobs := int64(len(jobs))
	var index int64 = 0
	var wg sync.WaitGroup

	for w := 0; w < a.cfg.Threads; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				idx := atomic.AddInt64(&index, 1) - 1
				if idx >= numJobs {
					break
				}
				job := jobs[idx]
				for tpSlParam, tpSlClose := range TpSlCloses {
					a.testTaskDirect(job.ic, job.signals, tpSlParam, tpSlClose.coefs, tpSlClose.indexes)
				}
			}
		}()
	}
	wg.Wait()

	close(a.Results)
	<-resultDoneSignal
}

func (a *Analyzer) testTaskDirect(ic IndicatorsCompare, signals []int, tpSlParam TpSlParam, coefs []float64, indexes []int) {
	// здесь берём один уровень. Если надо будет сравнивать 3 индикатора, то придётся позаморачиваться

	var (
		result       = 1.0
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
		currentMax := math.Float64frombits(atomic.LoadUint64(&a.maxCoefBits))
		if result > currentMax {
			if a.updateMaxCoef(result) {
				a.Results <- TaskResult{
					Task: Task{
						IndicatorsCompare: ic,
						TpSlParam:         tpSlParam,
					},
					Coef: result,
				}
			}
		}
	}
}

func (a *Analyzer) resultChannelHandler() <-chan struct{} {
	done := make(chan struct{})

	go func() {
		defer close(done)

		for r := range a.Results {
			fmt.Println(r)
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
	type compareJob struct {
		compareParams IndicatorsCompare
	}

	var jobs []compareJob
	for i := 0; i < len(IndicatorParamGroups)-1; i += 1 {
		currentParams := IndicatorParamGroups[i]

		for _, currentParam := range currentParams {
			for _, nextParam := range IndicatorParamGroups[i+1] {
				jobs = append(jobs, compareJob{
					compareParams: IndicatorsCompare{
						Indicator1Params: currentParam,
						Indicator2Params: nextParam,
					},
				})
			}
		}
	}

	numJobs := int64(len(jobs))
	var index int64 = 0
	var wg sync.WaitGroup
	var mu sync.Mutex

	for w := 0; w < a.cfg.Threads; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				idx := atomic.AddInt64(&index, 1) - 1
				if idx >= numJobs {
					break
				}
				job := jobs[idx]
				indexes := a.compareIndicators(job.compareParams)
				if indexes != nil {
					mu.Lock()
					IndicatorsCompares[job.compareParams] = indexes
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
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
		return source.AverageTwoSlices(a.quotes.Lows, a.quotes.Opens)
	case source.LC:
		return source.AverageTwoSlices(a.quotes.Lows, a.quotes.Closes)
	case source.LH:
		return source.AverageTwoSlices(a.quotes.Lows, a.quotes.Highs)
	case source.OC:
		return source.AverageTwoSlices(a.quotes.Opens, a.quotes.Closes)
	case source.OH:
		return source.AverageTwoSlices(a.quotes.Opens, a.quotes.Highs)
	case source.CH:
		return source.AverageTwoSlices(a.quotes.Closes, a.quotes.Highs)
	case source.LOC:
		return source.AverageThreeSlices(a.quotes.Lows, a.quotes.Opens, a.quotes.Closes)
	case source.LOH:
		return source.AverageThreeSlices(a.quotes.Lows, a.quotes.Opens, a.quotes.Highs)
	case source.LCH:
		return source.AverageThreeSlices(a.quotes.Lows, a.quotes.Closes, a.quotes.Highs)
	case source.OCH:
		return source.AverageThreeSlices(a.quotes.Opens, a.quotes.Closes, a.quotes.Highs)
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
	currentPrices := IndicatorPrices[p.Indicator1Params]
	nextPrices := IndicatorPrices[p.Indicator2Params]

	matchCount := 0
	for i := 0; i < a.ln-1; i++ {
		if nextPrices[i] > currentPrices[i] {
			matchCount++
		}
	}

	if matchCount < a.cfg.MinSignals {
		return nil
	}

	indexes := make([]int, matchCount)
	idx := 0
	for i := 0; i < a.ln-1; i++ {
		if nextPrices[i] > currentPrices[i] {
			indexes[idx] = i
			idx++
		}
	}

	return indexes
}
