package analyzer

func calculateDema(prices []float64, multiplier float64) []float64 {
	ema1 := calculateEma(prices, multiplier)

	ema2 := calculateEma(ema1, multiplier)

	result := make([]float64, len(prices))
	for i := 0; i < len(prices); i++ {
		result[i] = (2.0 * ema1[i]) - ema2[i]
	}

	return result
}
