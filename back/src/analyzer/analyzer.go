package analyzer

import (
	"fmt"
	"math"
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

// Service is responsible for analyzing candlestick market quotes.
type Service struct {
	quotes Quotes
}

// New creates a new instance of Analyzer Service.
func New(quotes Quotes) *Service {
	return &Service{
		quotes: quotes,
	}
}

// Length returns the total count of candlesticks in the Quotes struct.
func (s *Service) Length() int {
	return len(s.quotes.Timestamps)
}

func (s *Service) Run() {
	openedPrice := 0.0
	tpCnt := 0
	slCnt := 0

	values := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 8, 4, 6, 9}

	pr(values)
	pr(calculateSma(values, 4))
	pr(calculateEma(values, 1.5))
	pr(calculateDema(values, 1.5))
	pr(calculateTema(values, 1.5))
	pr(calculateTemaZero(values, 1.5))

	//fmt.Println(s.quotes.Closes)
	//fmt.Println(calculateSma(3, s.quotes.Closes))

	for i := range s.quotes.Closes {
		if openedPrice > 0 {
			if s.quotes.Highs[i] >= openedPrice+15 {
				tpCnt++
				openedPrice = 0.0
			} else if s.quotes.Lows[i] <= openedPrice-15 {
				slCnt++
				openedPrice = 0.0
			}
		} else {
			openedPrice = s.quotes.Opens[i]
		}
	}

	//fmt.Println(tpCnt, slCnt, len(s.quotes.Closes))
}

func pr(data []float64) {
	for _, v := range data {
		fmt.Printf("%6.2f ", v)
	}
	fmt.Printf("\n")
}

// SMA calculates the Simple Moving Average for a given period.
func (s *Service) SMA(period int) []float64 {
	closes := s.quotes.Closes
	if len(closes) < period {
		return nil
	}

	sma := make([]float64, len(closes))
	var sum float64
	for i := 0; i < period; i++ {
		sum += closes[i]
	}
	sma[period-1] = sum / float64(period)

	for i := period; i < len(closes); i++ {
		sum += closes[i] - closes[i-period]
		sma[i] = sum / float64(period)
	}

	return sma
}

// RSI calculates the Relative Strength Index for a given period.
func (s *Service) RSI(period int) []float64 {
	closes := s.quotes.Closes
	if len(closes) < period+1 {
		return nil
	}

	rsi := make([]float64, len(closes))

	// Calculate initial average gain and loss
	var avgGain, avgLoss float64
	for i := 1; i <= period; i++ {
		change := closes[i] - closes[i-1]
		if change > 0 {
			avgGain += change
		} else {
			avgLoss += math.Abs(change)
		}
	}
	avgGain /= float64(period)
	avgLoss /= float64(period)

	if avgLoss == 0 {
		rsi[period] = 100
	} else {
		rs := avgGain / avgLoss
		rsi[period] = 100 - (100 / (1 + rs))
	}

	// Calculate remaining RSI values using Wilder's smoothing technique
	for i := period + 1; i < len(closes); i++ {
		change := closes[i] - closes[i-1]
		var gain, loss float64
		if change > 0 {
			gain = change
		} else {
			loss = math.Abs(change)
		}

		avgGain = (avgGain*float64(period-1) + gain) / float64(period)
		avgLoss = (avgLoss*float64(period-1) + loss) / float64(period)

		if avgLoss == 0 {
			rsi[i] = 100
		} else {
			rs := avgGain / avgLoss
			rsi[i] = 100 - (100 / (1 + rs))
		}
	}

	return rsi
}
