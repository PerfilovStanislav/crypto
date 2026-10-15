package bot

import (
	"fmt"
	"math"
	"time"

	"trader/config"
	"trader/db"
	"trader/indicator"
	"trader/source"
)

// minSignalCandles is the least history the indicators are computed on.
const minSignalCandles = 100

// Signal reports the entry condition of the analyzer backtest
// (analyzer.CompareIndicators): indicator2 > indicator1 on the last candle.
// It is a state, not a crossing: the backtest enters on every candle where it
// holds while no trade is open, so the bot does the same.
func Signal(candles []db.Candle, s config.StrategyConfig) (bool, error) {
	if len(candles) < minSignalCandles {
		return false, fmt.Errorf("not enough candles: %d < %d", len(candles), minSignalCandles)
	}

	quotes := buildQuotes(candles)

	ind1, err := indicatorValues(quotes, s.Indicator1)
	if err != nil {
		return false, fmt.Errorf("indicator1: %w", err)
	}
	ind2, err := indicatorValues(quotes, s.Indicator2)
	if err != nil {
		return false, fmt.Errorf("indicator2: %w", err)
	}

	last := len(candles) - 1
	for _, v := range []float64{ind1[last], ind2[last]} {
		// e.g. ema with coef > 2 diverges; NaN would silently mean "no signal"
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false, fmt.Errorf("indicator value is %v, check coef", v)
		}
	}
	return ind2[last] > ind1[last], nil
}

func indicatorValues(quotes source.Quotes, ic config.IndicatorConfig) ([]float64, error) {
	prices := getPricesBySource(quotes, source.Type(ic.Source))
	if prices == nil {
		return nil, fmt.Errorf("unknown source %q", ic.Source)
	}
	vals := indicator.Type(ic.Type).CalculateIndicatorPrices(prices, ic.Coef)
	if vals == nil {
		return nil, fmt.Errorf("unknown type %q", ic.Type)
	}
	return vals, nil
}

// closedCandles returns candles up to and including the one that starts at
// expected. The candle counts as closed only when Bybit already lists a newer
// one after it; until then ok is false and the caller should poll again.
func closedCandles(candles []db.Candle, expected time.Time) (closed []db.Candle, ok bool) {
	for i, c := range candles {
		if c.Timestamp.Equal(expected) {
			if i == len(candles)-1 {
				return nil, false
			}
			return candles[:i+1], true
		}
	}
	return nil, false
}

// TpSlPrices converts analyzer offsets into absolute prices. In the analyzer
// config take_profit/stop_loss are in units of 10^-decimals and are applied to
// the entry price: high >= entry+tp closes in profit, low <= entry-sl in loss.
func TpSlPrices(entry, takeProfit, stopLoss float64, decimals int) (tp, sl float64) {
	factor := math.Pow10(decimals)
	return entry + takeProfit/factor, entry - stopLoss/factor
}

func buildQuotes(candles []db.Candle) source.Quotes {
	q := source.Quotes{}
	for _, c := range candles {
		q.Opens = append(q.Opens, c.Open)
		q.Highs = append(q.Highs, c.High)
		q.Lows = append(q.Lows, c.Low)
		q.Closes = append(q.Closes, c.Close)
		q.Volumes = append(q.Volumes, c.Volume)
		q.Timestamps = append(q.Timestamps, c.Timestamp)
	}
	return q
}

func getPricesBySource(quotes source.Quotes, t source.Type) []float64 {
	switch t {
	case source.L:
		return quotes.Lows
	case source.O:
		return quotes.Opens
	case source.C:
		return quotes.Closes
	case source.H:
		return quotes.Highs
	case source.V:
		return quotes.Volumes
	case source.LO:
		return source.AverageTwoSlices(quotes.Lows, quotes.Opens)
	case source.LC:
		return source.AverageTwoSlices(quotes.Lows, quotes.Closes)
	case source.LH:
		return source.AverageTwoSlices(quotes.Lows, quotes.Highs)
	case source.OC:
		return source.AverageTwoSlices(quotes.Opens, quotes.Closes)
	case source.OH:
		return source.AverageTwoSlices(quotes.Opens, quotes.Highs)
	case source.CH:
		return source.AverageTwoSlices(quotes.Closes, quotes.Highs)
	case source.LOC:
		return source.AverageThreeSlices(quotes.Lows, quotes.Opens, quotes.Closes)
	case source.LOH:
		return source.AverageThreeSlices(quotes.Lows, quotes.Opens, quotes.Highs)
	case source.LCH:
		return source.AverageThreeSlices(quotes.Lows, quotes.Closes, quotes.Highs)
	case source.OCH:
		return source.AverageThreeSlices(quotes.Opens, quotes.Closes, quotes.Highs)
	}
	return nil
}
