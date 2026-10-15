package exchange

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Instrument holds the spot trading rules of a symbol. Steps are kept with
// their decimal count so formatted values never carry float noise.
type Instrument struct {
	BaseCoin  string
	QuoteCoin string

	TickSize       float64
	BasePrecision  float64
	QuotePrecision float64
	MinOrderQty    float64
	MinOrderAmt    float64

	tickDecimals  int
	baseDecimals  int
	quoteDecimals int
}

// NewInstrument builds an Instrument from Bybit's string fields.
func NewInstrument(baseCoin, quoteCoin, tickSize, basePrecision, quotePrecision, minOrderQty, minOrderAmt string) (Instrument, error) {
	inst := Instrument{
		BaseCoin:      baseCoin,
		QuoteCoin:     quoteCoin,
		tickDecimals:  stepDecimals(tickSize),
		baseDecimals:  stepDecimals(basePrecision),
		quoteDecimals: stepDecimals(quotePrecision),
	}

	fields := []struct {
		dst *float64
		val string
		pos bool
	}{
		{&inst.TickSize, tickSize, true},
		{&inst.BasePrecision, basePrecision, true},
		{&inst.QuotePrecision, quotePrecision, true},
		{&inst.MinOrderQty, minOrderQty, false},
		{&inst.MinOrderAmt, minOrderAmt, false},
	}
	for _, f := range fields {
		v, err := strconv.ParseFloat(f.val, 64)
		if err != nil {
			return Instrument{}, fmt.Errorf("instrument: bad number %q: %w", f.val, err)
		}
		if f.pos && v <= 0 {
			return Instrument{}, fmt.Errorf("instrument: step must be > 0, got %q", f.val)
		}
		*f.dst = v
	}

	return inst, nil
}

// RoundPrice rounds a price to the nearest tick.
func (i Instrument) RoundPrice(p float64) float64 {
	return roundToStep(p, i.TickSize, i.tickDecimals)
}

// FormatPrice rounds a price to the nearest tick and formats it with the
// tick's precision.
func (i Instrument) FormatPrice(p float64) string {
	return strconv.FormatFloat(i.RoundPrice(p), 'f', i.tickDecimals, 64)
}

// FloorBaseQty floors a base coin quantity to the lot precision, so a sell
// never asks for more than is held.
func (i Instrument) FloorBaseQty(q float64) float64 {
	return floorToStep(q, i.BasePrecision, i.baseDecimals)
}

func (i Instrument) FormatBaseQty(q float64) string {
	return strconv.FormatFloat(i.FloorBaseQty(q), 'f', i.baseDecimals, 64)
}

// FloorQuoteQty floors a quote coin amount to the quote precision.
func (i Instrument) FloorQuoteQty(q float64) float64 {
	return floorToStep(q, i.QuotePrecision, i.quoteDecimals)
}

func (i Instrument) FormatQuoteQty(q float64) string {
	return strconv.FormatFloat(i.FloorQuoteQty(q), 'f', i.quoteDecimals, 64)
}

// stepDecimals returns the number of significant fractional digits of a step
// such as "0.0100" (2).
func stepDecimals(step string) int {
	dot := strings.IndexByte(step, '.')
	if dot < 0 {
		return 0
	}
	return len(strings.TrimRight(step[dot+1:], "0"))
}

// eps absorbs float noise like 0.4995/0.0001 = 4994.999999999999.
const eps = 1e-9

func floorToStep(v, step float64, decimals int) float64 {
	return cleanDecimals(math.Floor(v/step+eps)*step, decimals)
}

func roundToStep(v, step float64, decimals int) float64 {
	return cleanDecimals(math.Round(v/step)*step, decimals)
}

func cleanDecimals(v float64, decimals int) float64 {
	p := math.Pow10(decimals)
	return math.Round(v*p) / p
}
