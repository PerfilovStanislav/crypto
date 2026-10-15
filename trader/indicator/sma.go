package indicator

func calculateSma(prices []float64, n int) []float64 {
	result := make([]float64, len(prices))

	result[0] = prices[0]
	for i := 1; i < n; i++ {
		result[i] = (result[i-1]*float64(i) + prices[i]) / float64(i+1)
	}
	for i := n; i < len(prices); i++ {
		result[i] = result[i-1] + (prices[i]-prices[i-n])/float64(n)
	}

	return result
}
