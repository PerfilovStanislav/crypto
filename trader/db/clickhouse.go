package db

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"trader/config"
)

type Client struct {
	conn driver.Conn
}

type Candle struct {
	Symbol    string
	Timeframe string
	Timestamp time.Time
	Low       float64
	Open      float64
	Close     float64
	High      float64
	Volume    float64
}

func New(ctx context.Context, cfg config.DBConfig) (*Client, error) {
	var protocol clickhouse.Protocol
	if cfg.Port == 8123 {
		protocol = clickhouse.HTTP
	} else {
		protocol = clickhouse.Native
	}

	options := &clickhouse.Options{
		Addr:     []string{fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)},
		Protocol: protocol,
		Auth: clickhouse.Auth{
			Database: cfg.Database,
			Username: cfg.User,
			Password: cfg.Password,
		},
		DialTimeout:     time.Second * 10,
		ConnMaxLifetime: time.Hour,
		MaxOpenConns:    10,
		MaxIdleConns:    5,
	}

	conn, err := clickhouse.Open(options)
	if err != nil {
		return nil, fmt.Errorf("failed to open clickhouse connection: %w", err)
	}

	if err := conn.Ping(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("failed to ping clickhouse server: %w", err)
	}

	return &Client{
		conn: conn,
	}, nil
}

func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

func (c *Client) SaveCandles(ctx context.Context, candles []Candle) error {
	if len(candles) == 0 {
		return nil
	}

	batch, err := c.conn.PrepareBatch(ctx, "INSERT INTO market_data (symbol, timeframe, timestamp, low, open, close, high, volume)")
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}

	for _, candle := range candles {
		err := batch.Append(
			candle.Symbol,
			candle.Timeframe,
			candle.Timestamp,
			candle.Low,
			candle.Open,
			candle.Close,
			candle.High,
			candle.Volume,
		)
		if err != nil {
			return fmt.Errorf("append to batch: %w", err)
		}
	}

	if err := batch.Send(); err != nil {
		return fmt.Errorf("send batch: %w", err)
	}

	return nil
}

func (c *Client) LoadHistory(ctx context.Context, symbol, timeframe string, from time.Time) ([]Candle, error) {
	query := `
		SELECT symbol, timeframe, timestamp, low, open, close, high, volume
		FROM market_data
		WHERE symbol = $1 AND timeframe = $2 AND timestamp >= $3
		ORDER BY timestamp ASC
	`
	rows, err := c.conn.Query(ctx, query, symbol, timeframe, from)
	if err != nil {
		return nil, fmt.Errorf("query market_data: %w", err)
	}
	defer rows.Close()

	var candles []Candle
	for rows.Next() {
		var cndl Candle
		if err := rows.Scan(
			&cndl.Symbol,
			&cndl.Timeframe,
			&cndl.Timestamp,
			&cndl.Low,
			&cndl.Open,
			&cndl.Close,
			&cndl.High,
			&cndl.Volume,
		); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}
		candles = append(candles, cndl)
	}

	return candles, nil
}
