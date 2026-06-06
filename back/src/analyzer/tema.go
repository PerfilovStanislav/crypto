package analyzer

func calculateTema(prices []float64, multiplier float64) []float64 {
	ema1 := calculateEma(prices, multiplier)

	ema2 := calculateEma(ema1, multiplier)

	ema3 := calculateEma(ema2, multiplier)

	result := make([]float64, len(prices))
	for i := 0; i < len(prices); i++ {
		result[i] = (3.0 * ema1[i]) - (3.0 * ema2[i]) + ema3[i]
	}

	return result
}
