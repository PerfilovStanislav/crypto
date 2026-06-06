package analyzer

func calculateTemaZero(prices []float64, multiplier float64) []float64 {
	tema1 := calculateTema(prices, multiplier)

	tema2 := calculateTema(tema1, multiplier)

	result := make([]float64, len(prices))
	for i := 0; i < len(prices); i++ {
		result[i] = (2.0 * tema1[i]) - tema2[i]
	}

	return result
}
