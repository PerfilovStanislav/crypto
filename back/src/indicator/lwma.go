package indicator

// calculateLwma is the linearly weighted average, as MODE_LWMA in MT4: the
// newest price weighs n, the oldest in the window 1. The first n values use
// the prices available so far.
func calculateLwma(prices []float64, n int) []float64 {
	n = max(1, min(n, len(prices)))
	result := make([]float64, len(prices))
	for t := range prices {
		window := prices[max(0, t+1-n) : t+1]
		var weighted float64
		for i, p := range window {
			weighted += float64(i+1) * p
		}
		k := float64(len(window))
		result[t] = weighted / (k * (k + 1) / 2)
	}
	return result
}
