package server

import (
	"indicator"
	"net/http"
	"server/api"
	"source"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func mapIndicatorType(t api.IndicatorTypeEnum) indicator.Type {
	switch t {
	case api.IndicatorTypeEnum_INDICATOR_TYPE_SMA:
		return indicator.Sma
	case api.IndicatorTypeEnum_INDICATOR_TYPE_EMA:
		return indicator.Ema
	case api.IndicatorTypeEnum_INDICATOR_TYPE_DEMA:
		return indicator.Dema
	case api.IndicatorTypeEnum_INDICATOR_TYPE_TEMA:
		return indicator.Tema
	case api.IndicatorTypeEnum_INDICATOR_TYPE_TEMA_ZERO:
		return indicator.TemaZero
	default:
		return indicator.Ema
	}
}

func mapTimeframeName(tf api.TimeframeEnum) string {
	switch tf {
	case api.TimeframeEnum_TIMEFRAME_1M:
		return "1m"
	case api.TimeframeEnum_TIMEFRAME_5M:
		return "5m"
	case api.TimeframeEnum_TIMEFRAME_15M:
		return "15m"
	case api.TimeframeEnum_TIMEFRAME_30M:
		return "30m"
	case api.TimeframeEnum_TIMEFRAME_1H:
		return "1h"
	case api.TimeframeEnum_TIMEFRAME_4H:
		return "4h"
	case api.TimeframeEnum_TIMEFRAME_1D:
		return "1d"
	case api.TimeframeEnum_TIMEFRAME_1W:
		return "1w"
	default:
		return "4h"
	}
}

func mapTimeframeDuration(tf api.TimeframeEnum) time.Duration {
	switch tf {
	case api.TimeframeEnum_TIMEFRAME_1M:
		return time.Minute
	case api.TimeframeEnum_TIMEFRAME_5M:
		return 5 * time.Minute
	case api.TimeframeEnum_TIMEFRAME_15M:
		return 15 * time.Minute
	case api.TimeframeEnum_TIMEFRAME_30M:
		return 30 * time.Minute
	case api.TimeframeEnum_TIMEFRAME_1H:
		return time.Hour
	case api.TimeframeEnum_TIMEFRAME_4H:
		return 4 * time.Hour
	case api.TimeframeEnum_TIMEFRAME_1D:
		return 24 * time.Hour
	case api.TimeframeEnum_TIMEFRAME_1W:
		return 7 * 24 * time.Hour
	default:
		return 4 * time.Hour
	}
}

func ToProtoTimestamps(times []time.Time) []*timestamppb.Timestamp {
	result := make([]*timestamppb.Timestamp, len(times))

	for i, t := range times {
		result[i] = timestamppb.New(t)
	}

	return result
}

func getPricesBySource(sourceType api.SourceTypeEnum, l, o, c, h []float64) []float64 {
	switch sourceType {
	case api.SourceTypeEnum_SOURCE_TYPE_L:
		return l
	case api.SourceTypeEnum_SOURCE_TYPE_O:
		return o
	case api.SourceTypeEnum_SOURCE_TYPE_C:
		return c
	case api.SourceTypeEnum_SOURCE_TYPE_H:
		return h
	case api.SourceTypeEnum_SOURCE_TYPE_LO:
		return source.AverageTwoSlices(l, o)
	case api.SourceTypeEnum_SOURCE_TYPE_LC:
		return source.AverageTwoSlices(l, c)
	case api.SourceTypeEnum_SOURCE_TYPE_LH:
		return source.AverageTwoSlices(l, h)
	case api.SourceTypeEnum_SOURCE_TYPE_OC:
		return source.AverageTwoSlices(o, c)
	case api.SourceTypeEnum_SOURCE_TYPE_OH:
		return source.AverageTwoSlices(o, h)
	case api.SourceTypeEnum_SOURCE_TYPE_CH:
		return source.AverageTwoSlices(c, h)
	case api.SourceTypeEnum_SOURCE_TYPE_LOC:
		return source.AverageThreeSlices(l, o, c)
	case api.SourceTypeEnum_SOURCE_TYPE_LOH:
		return source.AverageThreeSlices(l, o, h)
	case api.SourceTypeEnum_SOURCE_TYPE_LCH:
		return source.AverageThreeSlices(l, c, h)
	case api.SourceTypeEnum_SOURCE_TYPE_OCH:
		return source.AverageThreeSlices(o, c, h)
	default:
		return c
	}
}

func calculateResponseMetrics(req *api.QuotesRequest, candles *api.Candles, times []*timestamppb.Timestamp) *api.QuotesResponse {
	lows := candles.L.Price
	opens := candles.O.Price
	closes := candles.C.Price
	highs := candles.H.Price
	n := len(closes)

	var ind1Prices []float64
	if req.Ind1 != nil {
		srcPrices := getPricesBySource(req.Ind1.Source, lows, opens, closes, highs)
		indType := mapIndicatorType(req.Ind1.Type)
		ind1Prices = indType.CalculateIndicatorPrices(srcPrices, req.Ind1.Coef)
	} else {
		ind1Prices = indicator.Ema.CalculateIndicatorPrices(closes, 10)
	}

	var ind2Prices []float64
	if req.Ind2 != nil {
		srcPrices := getPricesBySource(req.Ind2.Source, lows, opens, closes, highs)
		indType := mapIndicatorType(req.Ind2.Type)
		ind2Prices = indType.CalculateIndicatorPrices(srcPrices, req.Ind2.Coef)
	} else {
		ind2Prices = indicator.Ema.CalculateIndicatorPrices(closes, 20)
	}

	// Generate deals based on crossover and TP/SL
	var deals []*api.Deal
	openIndex := -1
	entryPrice := 0.0

	for i := 1; i < n; i++ {
		if openIndex == -1 {
			// Crossover Buy trigger
			if ind1Prices[i-1] <= ind2Prices[i-1] && ind1Prices[i] > ind2Prices[i] {
				openIndex = i
				entryPrice = closes[i]
			}
		} else {
			// Crossover Sell trigger
			crossDown := ind1Prices[i-1] >= ind2Prices[i-1] && ind1Prices[i] < ind2Prices[i]

			tpHit := false
			slHit := false
			if req.Takeprofit > 0 {
				tpHit = closes[i] >= entryPrice*(1+req.Takeprofit/100.0)
			}
			if req.Stoploss > 0 {
				slHit = closes[i] <= entryPrice*(1-req.Stoploss/100.0)
			}

			if crossDown || tpHit || slHit {
				deals = append(deals, &api.Deal{
					Open:  int32(openIndex),
					Close: int32(i),
				})
				openIndex = -1
			}
		}
	}

	if openIndex != -1 {
		deals = append(deals, &api.Deal{
			Open:  int32(openIndex),
			Close: int32(n - 1),
		})
	}

	return &api.QuotesResponse{
		Time:       times,
		Candles:    candles,
		Indicator1: &api.Prices{Price: ind1Prices},
		Indicator2: &api.Prices{Price: ind2Prices},
		Deals:      deals,
	}
}

func (s *Server) getQuotes(w http.ResponseWriter, body []byte) error {
	var req api.QuotesRequest
	if err := proto.Unmarshal(body, &req); err != nil {
		return err
	}

	return s.response(w, &api.QuotesResponse{
		Time: ToProtoTimestamps(s.quotes.Timestamps),
		Candles: &api.Candles{
			L: &api.Prices{Price: s.quotes.Lows},
			O: &api.Prices{Price: s.quotes.Opens},
			C: &api.Prices{Price: s.quotes.Closes},
			H: &api.Prices{Price: s.quotes.Highs},
		},
		Indicator1: &api.Prices{Price: s.quotes.Lows},
		Indicator2: &api.Prices{Price: s.quotes.Highs},
	})
}
