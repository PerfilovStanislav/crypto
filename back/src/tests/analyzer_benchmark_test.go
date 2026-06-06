package tests

import (
	"fmt"
	"testing"
)

func testInt64(x int64) int64 {
	return x + 1
}

var a int64 = 1000

func BenchmarkSimple(b *testing.B) {
	b.Run("test simple", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()

		var x int64
		for i := 0; i < b.N; i++ {
			a = x + 1
		}
	})
	fmt.Println(a)
}

func BenchmarkFun(b *testing.B) {
	b.Run("test fun", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()

		var x int64
		for i := 0; i < b.N; i++ {
			a = testInt64(x)
		}
	})
	fmt.Println(a)
}
