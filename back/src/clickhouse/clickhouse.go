package clickhouse

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

type Config struct {
	Host     string
	Port     int
	User     string
	Password string
	Database string
}

// Client represents a wrapper around ClickHouse driver connection pool.
type Client struct {
	conn driver.Conn
}

// New creates a new ClickHouse client connection.
func New(ctx context.Context, cfg Config) (*Client, error) {
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

	// Ping the server to ensure connection is established and credentials are valid
	if err := conn.Ping(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("failed to ping clickhouse server: %w", err)
	}

	return &Client{
		conn: conn,
	}, nil
}

// Conn returns the underlying driver.Conn for executing queries.
func (c *Client) Conn() driver.Conn {
	return c.conn
}

// Close closes the underlying ClickHouse connection.
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}
