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

	"github.com/fatih/color"
)

type TpSlParam struct {
	Tp float64
	Sl float64
}

type TpSlClose struct {
	Indexes   []int
	Coefs     []float64
	DrawDowns []float64
}

type Coef float64

type Analyzer struct {
	Cfg               config.AnalyzerConfig
	Quotes            source.Quotes
	ln                int
	Results           chan TaskResult
	maxProfitToDdBits uint64
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
	Task            Task
	Coef            float64
	MaxDrawdown     float64
	ProfitToDd      float64
	ProfitToCandles float64
	Wins            int
	Losses          int
}

var (
	TpSlCloses           = make(map[TpSlParam]TpSlClose)
	IndicatorPrices      = make(map[IndicatorParams][]float64)
	IndicatorParamGroups = make(map[int][]IndicatorParams)
	IndicatorsCompares   = make(map[IndicatorsCompare][]int)
)

func New(cfg config.AnalyzerConfig, quotes source.Quotes) *Analyzer {
	return &Analyzer{
		Cfg:     cfg,
		Quotes:  quotes,
		ln:      len(quotes.Opens),
		Results: make(chan TaskResult, cfg.Threads),
	}
}

func (a *Analyzer) updateMaxProfitToDd(val float64) bool {
	for {
		currentBits := atomic.LoadUint64(&a.maxProfitToDdBits)
		currentVal := math.Float64frombits(currentBits)
		if val <= currentVal {
			return false
		}
		newBits := math.Float64bits(val)
		if atomic.CompareAndSwapUint64(&a.maxProfitToDdBits, currentBits, newBits) {
			return true
		}
	}
}

func (a *Analyzer) Run() {
	runtime.GOMAXPROCS(a.Cfg.Threads)

	a.fillIndicatorParamGroups()
	a.fillTakeprofitStoplossParams()
	a.fillIndicatorsCompares()

	resultDoneSignal := a.resultChannelHandler()

	fmt.Printf("Jobs:%d\n", len(IndicatorsCompares)*len(TpSlCloses))
	fmt.Println(
		clr("     Profit pr", color.FgHiGreen),
		clr("  DrawDown dd", color.FgHiRed),
	)

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
	var completed int64 = 0
	var printedPct [11]int32
	var wg sync.WaitGroup

	for w := 0; w < a.Cfg.Threads; w++ {
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
					a.testTaskDirect(job.ic, job.signals, tpSlParam, tpSlClose.Indexes, tpSlClose.Coefs, tpSlClose.DrawDowns)
				}

				// Increment completed count and print progress bar at 10% steps
				completedVal := atomic.AddInt64(&completed, 1)
				if numJobs > 0 {
					pct := (completedVal * 10) / numJobs
					if pct > 0 && pct <= 10 {
						if atomic.CompareAndSwapInt32(&printedPct[pct], 0, 1) {
							pctVal := pct * 10
							bar := ""
							for i := 0; i < 10; i++ {
								if i < int(pct) {
									bar += "█"
								} else {
									bar += "░"
								}
							}
							fmt.Printf("Progress: [%s] %d%% (%d/%d jobs)\n", bar, pctVal, completedVal, numJobs)
						}
					}
				}
			}
		}()
	}
	wg.Wait()

	close(a.Results)
	<-resultDoneSignal
}

func (a *Analyzer) testTaskDirect(ic IndicatorsCompare, signals []int, tpSlParam TpSlParam, indexes []int, coefs, dds []float64) {
	// здесь берём один уровень. Если надо будет сравнивать 3 индикатора, то придётся позаморачиваться

	var (
		finalCoef    = 1.0
		closingIndex = -1
		maxCoef      = 1.0
		maxDrawdown  = 0.0
	)

	for i := 0; i < len(signals); i++ {
		openingSignalIndex := signals[i]
		if openingSignalIndex < closingIndex {
			continue
		}
		nextIdx := openingSignalIndex + 1

		finalCoef *= coefs[nextIdx]
		closingIndex = indexes[nextIdx]
		drawDown := dds[nextIdx]

		if finalCoef > maxCoef {
			maxCoef = finalCoef
		}

		if drawDown > maxDrawdown {
			maxDrawdown = drawDown
		}
	}

	if finalCoef > 2.0 {
		profitToDd := (finalCoef - 1.0) / maxDrawdown

		if profitToDd > 3.0 {
			currentMax := math.Float64frombits(atomic.LoadUint64(&a.maxProfitToDdBits))
			if profitToDd > currentMax {
				if a.updateMaxProfitToDd(profitToDd) {
					var (
						wins         = 0
						losses       = 0
						totalCandles = 0
						cIndex       = -1
					)
					for i := 0; i < len(signals); i++ {
						openingSignalIndex := signals[i]
						if openingSignalIndex < cIndex {
							continue
						}
						nextIdx := openingSignalIndex + 1
						tradeCoef := coefs[nextIdx]
						cIndex = indexes[nextIdx]

						if tradeCoef > 1.0 {
							wins++
						} else if tradeCoef < 1.0 {
							losses++
						}

						actualCloseIndex := cIndex
						if actualCloseIndex == len(coefs) {
							actualCloseIndex = len(coefs) - 1
						}
						totalCandles += actualCloseIndex - openingSignalIndex
					}

					profitPct := (finalCoef - 1) * 100
					maxDrawdownPct := maxDrawdown * 100
					var profitToCandles float64
					if totalCandles > 0 {
						profitToCandles = profitPct / float64(totalCandles)
					}

					reportedProfitToDd := profitToDd
					if maxDrawdown == 0 {
						reportedProfitToDd = math.Inf(1)
					}

					a.Results <- TaskResult{
						Task: Task{
							IndicatorsCompare: ic,
							TpSlParam:         tpSlParam,
						},
						Coef:            finalCoef,
						MaxDrawdown:     maxDrawdownPct,
						ProfitToDd:      reportedProfitToDd,
						ProfitToCandles: profitToCandles,
						Wins:            wins,
						Losses:          losses,
					}
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
	for g, indicatorGroup := range a.Cfg.Indicators {
		IndicatorParamGroups[g] = make([]IndicatorParams, 0)

		for _, ind := range indicatorGroup {
			for _, src := range ind.Sources {
				prices := a.GetPricesBySource(src)

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
	for tp := a.Cfg.Takeprofit.Start; tp <= a.Cfg.Takeprofit.End; tp += a.Cfg.Takeprofit.Step {
		for sl := a.Cfg.Stoploss.Start; sl <= a.Cfg.Stoploss.End; sl += a.Cfg.Stoploss.Step {
			param := TpSlParam{
				Tp: tp,
				Sl: sl,
			}
			if !a.hasEnoughCloses(param) {
				break
			}

			TpSlCloses[param] = CalculateClosings(a.Quotes, param, a.Cfg.Commission)
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

	for w := 0; w < a.Cfg.Threads; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				idx := atomic.AddInt64(&index, 1) - 1
				if idx >= numJobs {
					break
				}
				job := jobs[idx]
				compareParams := job.compareParams
				currentPrices := IndicatorPrices[compareParams.Indicator1Params]
				nextPrices := IndicatorPrices[compareParams.Indicator2Params]
				indexes := a.CompareIndicators(currentPrices, nextPrices)
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

func (a *Analyzer) GetPricesBySource(t source.Type) []float64 {
	switch t {
	case source.L:
		return a.Quotes.Lows
	case source.O:
		return a.Quotes.Opens
	case source.C:
		return a.Quotes.Closes
	case source.H:
		return a.Quotes.Highs
	case source.V:
		return a.Quotes.Volumes
	case source.LO:
		return source.AverageTwoSlices(a.Quotes.Lows, a.Quotes.Opens)
	case source.LC:
		return source.AverageTwoSlices(a.Quotes.Lows, a.Quotes.Closes)
	case source.LH:
		return source.AverageTwoSlices(a.Quotes.Lows, a.Quotes.Highs)
	case source.OC:
		return source.AverageTwoSlices(a.Quotes.Opens, a.Quotes.Closes)
	case source.OH:
		return source.AverageTwoSlices(a.Quotes.Opens, a.Quotes.Highs)
	case source.CH:
		return source.AverageTwoSlices(a.Quotes.Closes, a.Quotes.Highs)
	case source.LOC:
		return source.AverageThreeSlices(a.Quotes.Lows, a.Quotes.Opens, a.Quotes.Closes)
	case source.LOH:
		return source.AverageThreeSlices(a.Quotes.Lows, a.Quotes.Opens, a.Quotes.Highs)
	case source.LCH:
		return source.AverageThreeSlices(a.Quotes.Lows, a.Quotes.Closes, a.Quotes.Highs)
	case source.OCH:
		return source.AverageThreeSlices(a.Quotes.Opens, a.Quotes.Closes, a.Quotes.Highs)
	}

	return nil
}

func (a *Analyzer) hasEnoughCloses(param TpSlParam) bool {
	openedPrice := 0.0
	closes := 0

	for i, v := range a.Quotes.Opens {
		if openedPrice == 0.0 {
			openedPrice = v
		}

		if a.Quotes.Highs[i] >= openedPrice+param.Tp {
			closes++ // for long positions
			if closes >= a.Cfg.MinCloses {
				return true
			}
			openedPrice = 0.0
		} else if a.Quotes.Lows[i] <= openedPrice-param.Sl {
			openedPrice = 0.0
		}
	}

	return false
}

func CalculateClosings(quotes source.Quotes, p TpSlParam, comission float64) TpSlClose {
	ln := len(quotes.Lows)
	c := TpSlClose{
		Indexes:   make([]int, ln),
		Coefs:     make([]float64, ln),
		DrawDowns: make([]float64, ln),
	}
	cms := (1 - comission) * (1 - comission)

level2:
	for i, openedPrice := range quotes.Opens {
		lowestPrice := quotes.Lows[i]
		for k := i; k < ln; k++ {
			if lowestPrice > quotes.Lows[k] {
				lowestPrice = quotes.Lows[k]
			}

			if quotes.Lows[k] <= openedPrice-p.Sl {
				coef := cms * (openedPrice - p.Sl) / openedPrice
				c.Indexes[i] = k
				c.Coefs[i] = coef
				c.DrawDowns[i] = p.Sl / openedPrice
				continue level2
			} else if quotes.Highs[k] >= openedPrice+p.Tp {
				coef := cms * (openedPrice + p.Tp) / openedPrice
				c.Indexes[i] = k
				c.Coefs[i] = coef
				c.DrawDowns[i] = 1.0 - lowestPrice/openedPrice
				continue level2
			}
		}
		for k := i; k < ln; k++ {
			c.Indexes[k] = ln
			c.Coefs[k] = 1.0
			c.DrawDowns[k] = 0.0
		}
	}

	return c
}

func (a *Analyzer) CompareIndicators(currentPrices, nextPrices []float64) []int {
	matchCount := 0
	for i := 0; i < a.ln-1; i++ {
		if nextPrices[i] > currentPrices[i] {
			matchCount++
		}
	}

	if matchCount < a.Cfg.MinSignals {
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
