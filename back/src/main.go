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
	"runtime"
	"server"
	"shutdown"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"
)

var m runtime.MemStats

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "application stopped with error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("application is stopped")
}

func run() error {
	runtime.ReadMemStats(&m)

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

	if err = syncMarketData(ctx, ch, log, cfg.Analyzer); err != nil {
		return fmt.Errorf("market data sync failed: %w", err)
	}

	// Загрузка данных за последние 2 года в структуру Quotes
	quotes, err := loadMarketData(ctx, ch, cfg.Analyzer.Pair, cfg.Analyzer.Timeframe)
	if err != nil {
		return fmt.Errorf("failed to load market data: %w", err)
	}

	srv := server.New(log, cfg.Http, quotes)
	graceful.Add(srv)
	srv.Run(gCtx, g)

	// Инициализация сервиса Analyzer и проведение анализа котировок
	az := analyzer.New(cfg.Analyzer, quotes)

	start := time.Now()

	az.Run()

	fmt.Printf("Execution time: %s\n", time.Since(start))
	//fmt.Printf("Alloc = %d MiB\n", bToMb(m.Alloc))
	//fmt.Printf("TotalAlloc = %d MiB\n", bToMb(m.TotalAlloc))
	//fmt.Printf("Sys = %d MiB\n", bToMb(m.Sys))
	//fmt.Printf("NumGC = %d\n", m.NumGC)

	log.Info("waiting for background tasks or shutdown signal...")

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

//func bToMb(b uint64) uint64 {
//	return b / 1024 / 1024
//}
