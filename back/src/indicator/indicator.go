package indicator

type Type string

const (
	Sma      Type = "sma"
	Ema      Type = "ema"
	Dema     Type = "dema"
	Tema     Type = "tema"
	TemaZero Type = "temaZero"
)

func (t Type) CalculateIndicatorPrices(prices []float64, coef float64) []float64 {
	switch t {
	case Sma:
		return calculateSma(prices, int(coef))
	case Ema:
		return calculateEma(prices, coef)
	case Dema:
		return calculateDema(prices, coef)
	case Tema:
		return calculateTema(prices, coef)
	case TemaZero:
		return calculateTemaZero(prices, coef)
	}

	return nil
}
