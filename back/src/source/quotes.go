package source

import "time"

type Quotes struct {
	Timestamps []time.Time
	Lows       []float64
	Opens      []float64
	Closes     []float64
	Highs      []float64
	Volumes    []float64
}

func AverageTwoSlices(s1, s2 []float64) []float64 {
	res := make([]float64, len(s1))
	for i := range s1 {
		res[i] = (s1[i] + s2[i]) / 2.0
	}
	return res
}

func AverageThreeSlices(s1, s2, s3 []float64) []float64 {
	res := make([]float64, len(s1))
	for i := range s1 {
		res[i] = (s1[i] + s2[i] + s3[i]) / 3.0
	}
	return res
}

func ReverseSlice[T any](s []T) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}
