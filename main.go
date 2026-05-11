package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/svdba/assist/config"
	"github.com/svdba/assist/internal/bot"
	"github.com/svdba/assist/internal/logging"
	"github.com/svdba/assist/internal/secretary"
	"github.com/svdba/assist/version"
)

func main() {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		panic(fmt.Sprintf("Failed to load config: %v", err))
	}

	// Initialize logging
	logging.Init(logging.Config{
		Level:      cfg.LogLevel,
		Format:     cfg.LogFormat,
		AppName:    version.AppName,
		WithCaller: true,
	})

	ctx := context.Background()
	logging.Info(ctx, "Starting secretary bot",
		"version", version.Version,
		"build_date", version.BuildDate,
		"build_sha", version.BuildSHA)

	// Create secretary core
	core, err := secretary.NewCore(cfg.Secretary)
	if err != nil {
		panic(fmt.Sprintf("Failed to create core: %v", err))
	}
	defer core.Close()

	// Create Telegram bot (only if token is provided)
	var telegramBot *bot.Bot
	if cfg.TelegramToken != "" {
		telegramBot, err = bot.NewBot(cfg.TelegramToken, cfg.TelegramAPIURL, core)
		if err != nil {
			logging.Fatal(ctx, "Failed to create bot", "error", err)
		}
	} else {
		logging.Warn(ctx, "Telegram bot disabled: TELEGRAM_BOT_TOKEN is not set")
	}

	// Run bot with graceful shutdown
	ctx, cancel := context.WithCancel(ctx)

	if telegramBot != nil {
		go func() {
			if err := telegramBot.Run(ctx); err != nil {
				logging.Error(ctx, "Bot stopped with error", "error", err)
			}
		}()
	}

	logging.Info(ctx, "Secretary bot is running")

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logging.Info(ctx, "Shutting down gracefully...")
	cancel()

	if telegramBot != nil {
		telegramBot.Stop()
	}
}
