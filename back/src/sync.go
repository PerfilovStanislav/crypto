package main

import (
	"analyzer"
	"clickhouse"
	"context"
	"encoding/json"
	"fmt"
	"logger"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

type BybitKlineResponse struct {
	RetCode int    `json:"retCode"`
	RetMsg  string `json:"retMsg"`
	Result  struct {
		Category string     `json:"category"`
		Symbol   string     `json:"symbol"`
		List     [][]string `json:"list"`
	} `json:"result"`
}

type Candle struct {
	Symbol    string
	Timeframe string
	Timestamp time.Time
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Volume    float64
}

// syncMarketData queries ClickHouse for the maximum timestamp, fetches missing
// historical SOLUSDT H4 quotes from Bybit, and batch inserts them back.
func syncMarketData(ctx context.Context, ch *clickhouse.Client, log *logger.Logger) error {
	var maxTime time.Time
	err := ch.Conn().QueryRow(ctx, `
		SELECT max(timestamp) FROM "default".market_data WHERE symbol = ? AND timeframe = ?
	`, "SOLUSDT", "4h").Scan(&maxTime)
	if err != nil {
		return fmt.Errorf("failed to query max timestamp: %w", err)
	}

	var startTime time.Time
	if maxTime.Year() < 1980 {
		startTime = time.Now().AddDate(-2, 0, 0)
		log.Info("no market data found in clickhouse, syncing full 2 years of history", "start_time", startTime.Format("2006-01-02 15:04:05"))
	} else {
		startTime = maxTime
		log.Info("found existing market data in clickhouse", "max_time", startTime.Format("2006-01-02 15:04:05"))
	}

	targetStartMs := startTime.UnixMilli()
	targetEndMs := time.Now().UnixMilli()

	if targetEndMs <= targetStartMs+14400000 { // 4 hours in ms
		log.Info("market data is already up-to-date")
		return nil
	}

	log.Info("syncing SOLUSDT 4h klines from bybit...", "from", startTime.Format("2006-01-02 15:04:05"), "to", time.Now().Format("2006-01-02 15:04:05"))

	currentEndMs := targetEndMs
	var allCandles [][]string

	for currentEndMs > targetStartMs {
		log.Info("fetching batch from bybit kline API...", "end_time", time.UnixMilli(currentEndMs).UTC().Format("2006-01-02 15:04:05"))
		candles, err := fetchBybitKlines("SOLUSDT", "240", targetStartMs, currentEndMs)
		if err != nil {
			return fmt.Errorf("failed to fetch klines from bybit: %w", err)
		}
		if len(candles) == 0 {
			break
		}

		allCandles = append(allCandles, candles...)

		// Bybit returns newest first, so the oldest candle is at the end of the slice
		oldestStr := candles[len(candles)-1][0]
		oldestMs, err := strconv.ParseInt(oldestStr, 10, 64)
		if err != nil {
			return fmt.Errorf("failed to parse oldest candle timestamp: %w", err)
		}

		if oldestMs <= targetStartMs {
			break
		}

		// Update currentEndMs for next request (subtract 1 ms to prevent overlap)
		currentEndMs = oldestMs - 1

		// Polite delay to respect Rate Limit
		time.Sleep(100 * time.Millisecond)
	}

	log.Info("fetched all candles from bybit", "raw_count", len(allCandles))

	// Parse and filter out candles already in ClickHouse
	var candlesToInsert []Candle
	for _, raw := range allCandles {
		candle, err := parseCandle(raw)
		if err != nil {
			log.Warn("skipping invalid candle", "error", err)
			continue
		}

		// Only insert candles strictly newer than maxTime and older than current time
		if candle.Timestamp.After(maxTime) && candle.Timestamp.Before(time.Now()) {
			candlesToInsert = append(candlesToInsert, candle)
		}
	}

	log.Info("filtered candles for insertion", "to_insert_count", len(candlesToInsert))

	if len(candlesToInsert) == 0 {
		log.Info("no new unique candles to insert")
		return nil
	}

	// Insert into ClickHouse in batch
	if err := saveCandles(ctx, ch.Conn(), candlesToInsert); err != nil {
		return fmt.Errorf("failed to save candles to clickhouse: %w", err)
	}

	log.Info("successfully saved candles to clickhouse", "inserted_count", len(candlesToInsert))
	return nil
}

// fetchBybitKlines gets candles from Bybit v5 public API.
func fetchBybitKlines(symbol, interval string, startMs, endMs int64) ([][]string, error) {
	u, err := url.Parse("https://api.bybit.com/v5/market/kline")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("category", "spot")
	q.Set("symbol", symbol)
	q.Set("interval", interval)
	q.Set("limit", "1000")
	q.Set("start", strconv.FormatInt(startMs, 10))
	q.Set("end", strconv.FormatInt(endMs, 10))
	u.RawQuery = q.Encode()

	resp, err := http.Get(u.String())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected http status: %d", resp.StatusCode)
	}

	var apiResp BybitKlineResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, err
	}

	if apiResp.RetCode != 0 {
		return nil, fmt.Errorf("bybit API returned error code %d: %s", apiResp.RetCode, apiResp.RetMsg)
	}

	return apiResp.Result.List, nil
}

// parseCandle converts Bybit API candle array to a Candle struct.
func parseCandle(raw []string) (Candle, error) {
	if len(raw) < 6 {
		return Candle{}, fmt.Errorf("insufficient fields, got %d expected at least 6", len(raw))
	}

	ms, err := strconv.ParseInt(raw[0], 10, 64)
	if err != nil {
		return Candle{}, fmt.Errorf("invalid timestamp %q: %w", raw[0], err)
	}
	ts := time.UnixMilli(ms).UTC()

	open, err := strconv.ParseFloat(raw[1], 64)
	if err != nil {
		return Candle{}, fmt.Errorf("invalid open price %q: %w", raw[1], err)
	}

	high, err := strconv.ParseFloat(raw[2], 64)
	if err != nil {
		return Candle{}, fmt.Errorf("invalid high price %q: %w", raw[2], err)
	}

	low, err := strconv.ParseFloat(raw[3], 64)
	if err != nil {
		return Candle{}, fmt.Errorf("invalid low price %q: %w", raw[3], err)
	}

	closePrice, err := strconv.ParseFloat(raw[4], 64)
	if err != nil {
		return Candle{}, fmt.Errorf("invalid close price %q: %w", raw[4], err)
	}

	volume, err := strconv.ParseFloat(raw[5], 64)
	if err != nil {
		return Candle{}, fmt.Errorf("invalid volume %q: %w", raw[5], err)
	}

	return Candle{
		Symbol:    "SOLUSDT",
		Timeframe: "4h",
		Timestamp: ts,
		Open:      open,
		High:      high,
		Low:       low,
		Close:     closePrice,
		Volume:    volume,
	}, nil
}

// saveCandles uses clickhouse-go native Batch API to insert candles.
func saveCandles(ctx context.Context, conn driver.Conn, candles []Candle) error {
	batch, err := conn.PrepareBatch(ctx, "INSERT INTO default.market_data (symbol, timeframe, timestamp, low, open, close, high, volume)")
	if err != nil {
		return fmt.Errorf("prepare batch failed: %w", err)
	}

	for _, c := range candles {
		err = batch.Append(
			c.Symbol,
			c.Timeframe,
			c.Timestamp,
			c.Low,
			c.Open,
			c.Close,
			c.High,
			c.Volume,
		)
		if err != nil {
			return fmt.Errorf("append to batch failed: %w", err)
		}
	}

	if err = batch.Send(); err != nil {
		return fmt.Errorf("send batch failed: %w", err)
	}

	return nil
}

// loadMarketData queries database for quotes of selected currency and timeframe for last 2 years.
func loadMarketData(ctx context.Context, ch *clickhouse.Client, symbol, timeframe string) (analyzer.Quotes, error) {
	//period := time.Now().AddDate(-2, 0, 0)
	period := time.Now().AddDate(0, 0, -20)
	query := `
		SELECT timestamp, low, open, close, high, volume 
		FROM "default".market_data 
		WHERE symbol = ? AND timeframe = ? AND timestamp >= ?
		ORDER BY timestamp
	`
	rows, err := ch.Conn().Query(ctx, query, symbol, timeframe, period)
	if err != nil {
		return analyzer.Quotes{}, fmt.Errorf("failed to query market data: %w", err)
	}
	defer rows.Close()

	var res analyzer.Quotes

	for rows.Next() {
		var (
			ts                                time.Time
			low, open, closeVal, high, volume float64
		)
		if err = rows.Scan(&ts, &low, &open, &closeVal, &high, &volume); err != nil {
			return analyzer.Quotes{}, fmt.Errorf("failed to scan row: %w", err)
		}
		res.Timestamps = append(res.Timestamps, ts)
		res.Lows = append(res.Lows, low)
		res.Opens = append(res.Opens, open)
		res.Closes = append(res.Closes, closeVal)
		res.Highs = append(res.Highs, high)
		res.Volumes = append(res.Volumes, volume)
	}

	if err = rows.Err(); err != nil {
		return analyzer.Quotes{}, fmt.Errorf("error during row iteration: %w", err)
	}

	return res, nil
}
