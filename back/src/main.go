package main

import (
	"clickhouse"
	"config"
	"context"
	"fmt"
	"logger"
	"os"
	"os/signal"
	"shutdown"
	"syscall"

	"golang.org/x/sync/errgroup"
)

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "application stopped with error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("application is stopped")
}

func run() error {
	cfg := config.Init()
	log := logger.New(cfg.Log)

	graceful := shutdown.New(log)
	graceful.Add(log)
	defer graceful.CloseAll()

	ctx, stop := waitKillSignal()
	defer stop()

	g, gCtx := errgroup.WithContext(ctx)

	ch, err := clickhouse.New(ctx, clickhouse.Config{
		Host:     cfg.Db.Host,
		Port:     cfg.Db.Port,
		User:     cfg.Db.User,
		Password: cfg.Db.Password,
		Database: cfg.Db.Database,
	})
	if err != nil {
		return fmt.Errorf("clickhouse initialization error: %w", err)
	}
	graceful.Add(ch)

	log.Info("application started")

	// Синхронизация котировок SOLUSDT H4
	log.Info("starting market data sync...")
	if err = syncMarketData(ctx, ch, log); err != nil {
		return fmt.Errorf("market data sync failed: %w", err)
	}
	log.Info("market data sync completed successfully")

	// Загрузка данных за последние 2 года в 6 массивов
	log.Info("loading market data for the last 2 years from clickhouse...")
	timestamps, lows, opens, closes, highs, volumes, err := loadMarketData(ctx, ch, "SOLUSDT", "4h")
	if err != nil {
		return fmt.Errorf("failed to load market data: %w", err)
	}

	log.Info("successfully loaded data into 6 slices",
		"timestamps_count", len(timestamps),
		"lows_count", len(lows),
		"opens_count", len(opens),
		"closes_count", len(closes),
		"highs_count", len(highs),
		"volumes_count", len(volumes),
	)
	if len(timestamps) > 0 {
		log.Info("data slice samples",
			"first_ts", timestamps[0].Format("2006-01-02 15:04:05"),
			"first_close", closes[0],
			"last_ts", timestamps[len(timestamps)-1].Format("2006-01-02 15:04:05"),
			"last_close", closes[len(closes)-1],
		)
	}

	<-gCtx.Done()
	stop()
	log.Info("shutdown signal received, waiting for background tasks to finish...")

	// Ждём завершения всех горутин
	if err = g.Wait(); err != nil {
		log.Error("background task failed", "error", err)
	}

	return nil
}

func waitKillSignal() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
		syscall.SIGINT,
	)
}
