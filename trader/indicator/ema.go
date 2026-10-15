package indicator

func calculateEma(prices []float64, multiplier float64) []float64 {
	result := make([]float64, len(prices))

	result[0] = prices[0]

	for i := 1; i < len(prices); i++ {
		result[i] = (prices[i] * multiplier) + (result[i-1] * (1.0 - multiplier))
	}

	return result
}
