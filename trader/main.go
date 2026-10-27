package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"trader/bot"
	"trader/config"
	"trader/exchange"
	"trader/telegram"
)

func main() {
	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bybitClient := exchange.NewClient(cfg.Bybit)

	b := bot.New(cfg, bybitClient, telegram.New(cfg.Telegram))

	// Graceful shutdown
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan
		log.Println("Shutting down...")
		cancel()
	}()

	if err := b.Run(ctx); err != nil {
		log.Fatalf("Bot stopped with error: %v", err)
	}
}
