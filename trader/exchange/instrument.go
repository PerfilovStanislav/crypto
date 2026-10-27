package exchange

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Instrument holds the USDT perpetual trading rules of a symbol. Steps are kept
// with their decimal count so formatted values never carry float noise.
type Instrument struct {
	BaseCoin  string
	QuoteCoin string

	TickSize    float64
	QtyStep     float64
	MinOrderQty float64
	// MinNotional is the smallest order value in QuoteCoin (5 USDT).
	MinNotional float64

	tickDecimals int
	qtyDecimals  int
}

// NewInstrument builds an Instrument from Bybit's string fields.
func NewInstrument(baseCoin, quoteCoin, tickSize, qtyStep, minOrderQty, minNotional string) (Instrument, error) {
	inst := Instrument{
		BaseCoin:     baseCoin,
		QuoteCoin:    quoteCoin,
		tickDecimals: stepDecimals(tickSize),
		qtyDecimals:  stepDecimals(qtyStep),
	}

	fields := []struct {
		dst *float64
		val string
		pos bool
	}{
		{&inst.TickSize, tickSize, true},
		{&inst.QtyStep, qtyStep, true},
		{&inst.MinOrderQty, minOrderQty, false},
		{&inst.MinNotional, minNotional, false},
	}
	for _, f := range fields {
		v, err := parseOptionalFloat(f.val)
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

// FloorQty floors a base coin quantity to the qty step.
func (i Instrument) FloorQty(q float64) float64 {
	return floorToStep(q, i.QtyStep, i.qtyDecimals)
}

func (i Instrument) FormatQty(q float64) string {
	return strconv.FormatFloat(i.FloorQty(q), 'f', i.qtyDecimals, 64)
}

// MinQty is the smallest quantity Bybit accepts at price: at least
// minOrderQty and an order value of at least minNotionalValue.
func (i Instrument) MinQty(price float64) float64 {
	byValue := cleanDecimals(math.Ceil(i.MinNotional/price/i.QtyStep-eps)*i.QtyStep, i.qtyDecimals)
	return math.Max(i.MinOrderQty, byValue)
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
