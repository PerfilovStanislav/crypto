package main

import (
	"analyzer"
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

	// Загрузка данных за последние 2 года в структуру Quotes
	log.Info("loading market data for the last 2 years from clickhouse...")
	quotes, err := loadMarketData(ctx, ch, "SOLUSDT", "4h")
	if err != nil {
		return fmt.Errorf("failed to load market data: %w", err)
	}

	// Инициализация сервиса Analyzer и проведение анализа котировок
	log.Info("initializing analyzer service...")
	az := analyzer.New(quotes)
	az.Run()

	stop()

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
