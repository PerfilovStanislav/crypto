package server

import (
	"analyzer"
	"fmt"
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

func mapSources(reqSource api.SourceTypeEnum) source.Type {
	switch reqSource {
	case api.SourceTypeEnum_SOURCE_TYPE_L:
		return source.L
	case api.SourceTypeEnum_SOURCE_TYPE_O:
		return source.O
	case api.SourceTypeEnum_SOURCE_TYPE_C:
		return source.C
	case api.SourceTypeEnum_SOURCE_TYPE_H:
		return source.H
	case api.SourceTypeEnum_SOURCE_TYPE_LO:
		return source.LO
	case api.SourceTypeEnum_SOURCE_TYPE_LC:
		return source.LC
	case api.SourceTypeEnum_SOURCE_TYPE_LH:
		return source.LH
	case api.SourceTypeEnum_SOURCE_TYPE_OC:
		return source.OC
	case api.SourceTypeEnum_SOURCE_TYPE_OH:
		return source.OH
	case api.SourceTypeEnum_SOURCE_TYPE_CH:
		return source.CH
	case api.SourceTypeEnum_SOURCE_TYPE_LOC:
		return source.LOC
	case api.SourceTypeEnum_SOURCE_TYPE_LOH:
		return source.LOH
	case api.SourceTypeEnum_SOURCE_TYPE_LCH:
		return source.LCH
	case api.SourceTypeEnum_SOURCE_TYPE_OCH:
		return source.OCH
	default:
		return source.L
	}
}

func (s *Server) getQuotes(w http.ResponseWriter, body []byte) error {
	var req api.QuotesRequest
	if err := proto.Unmarshal(body, &req); err != nil {
		return err
	}

	p := analyzer.TpSlParam{
		Tp: req.Takeprofit,
		Sl: req.Stoploss,
	}
	TpSlCloses := analyzer.CalculateClosings(s.az.Quotes, p, s.az.Cfg.Commission)

	ind1Prices := s.getIndicatorCalculatedPrices(req.Ind1)
	ind2Prices := s.getIndicatorCalculatedPrices(req.Ind2)
	signals := s.az.CompareIndicators(ind1Prices, ind2Prices)

	var (
		result       = 1.0
		closingIndex = -1
		deals        = make([]*api.Deal, 0)
	)

	for i := 0; i < len(signals); i++ {
		openingSignalIndex := signals[i]
		if openingSignalIndex < closingIndex {
			continue
		}
		result *= TpSlCloses.Coefs[openingSignalIndex+1] // открытие происходит на следующей свече
		closingIndex = TpSlCloses.Indexes[openingSignalIndex+1]
		deals = append(deals, &api.Deal{
			Open:  int32(openingSignalIndex + 1),
			Close: int32(closingIndex),
		})
	}

	fmt.Println(result)

	return s.response(w, &api.QuotesResponse{
		Time: ToProtoTimestamps(s.az.Quotes.Timestamps),
		Candles: &api.Candles{
			L: &api.Prices{Price: s.az.Quotes.Lows},
			O: &api.Prices{Price: s.az.Quotes.Opens},
			C: &api.Prices{Price: s.az.Quotes.Closes},
			H: &api.Prices{Price: s.az.Quotes.Highs},
		},
		Indicator1: &api.Prices{Price: ind1Prices},
		Indicator2: &api.Prices{Price: ind2Prices},
		Deals:      deals,
	})
}

func (s *Server) getIndicatorCalculatedPrices(ind *api.Indicator) []float64 {
	return mapIndicatorType(ind.Type).CalculateIndicatorPrices(
		s.az.GetPricesBySource(mapSources(ind.Source)), ind.Coef,
	)
}
