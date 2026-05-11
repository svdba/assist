// internal/bot/bot.go

package bot

import (
	"context"
	"fmt"
	"net/http"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/svdba/assist/internal/logging"
	"github.com/svdba/assist/internal/secretary"
)

// Bot represents a Telegram bot
type Bot struct {
	api          *tgbotapi.BotAPI
	core         *secretary.Core
	updates      tgbotapi.UpdatesChannel
	done         chan struct{}
	pendingPurge *pendingPurge
}

type pendingPurge struct {
	chatID int64
	date   *time.Time
}

// NewBot creates and initializes a new Telegram bot
func NewBot(token string, apiURL string, core *secretary.Core) (*Bot, error) {
	if token == "" {
		return nil, fmt.Errorf("telegram bot token is required")
	}

	var api *tgbotapi.BotAPI
	var err error

	if apiURL != "" && apiURL != "https://api.telegram.org/" {
		// Ensure apiURL ends with slash
		if apiURL[len(apiURL)-1] != '/' {
			apiURL += "/"
		}
		// Use custom API endpoint (e.g., for debugging with reverse proxy)
		api, err = tgbotapi.NewBotAPIWithClient(token, apiURL+"bot%s/%s", &http.Client{})
	} else {
		api, err = tgbotapi.NewBotAPI(token)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create bot: %w", err)
	}

	ctx := context.Background()
	logging.Info(ctx, "Telegram bot authorized",
		"username", api.Self.UserName,
		"id", api.Self.ID,
		"api_url", apiURL)

	return &Bot{
		api:  api,
		core: core,
		done: make(chan struct{}),
	}, nil
}

// Run starts the bot and begins processing updates
func (b *Bot) Run(ctx context.Context) error {
	// Configure update listener
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := b.api.GetUpdatesChan(u)
	b.updates = updates

	logging.Info(ctx, "Bot started listening for updates")

	for {
		select {
		case <-ctx.Done():
			logging.Info(ctx, "Bot context cancelled, stopping...")
			return nil
		case update, ok := <-updates:
			if !ok {
				logging.Info(ctx, "Updates channel closed")
				return nil
			}
			b.handleUpdate(ctx, update)
		}
	}
}

// Stop gracefully stops the bot
func (b *Bot) Stop() {
	if b.api != nil {
		b.api.StopReceivingUpdates()
	}
}

// handleUpdate processes incoming Telegram updates
func (b *Bot) handleUpdate(ctx context.Context, update tgbotapi.Update) {
	// Handle messages
	if update.Message != nil {
		// Check if user is authorized
		if b.authorizeUser(update.Message.From.ID) {
			b.handleMessage(ctx, update.Message)
		} else {
			logging.Error(ctx, "Unauthorized access attempt",
				"id", update.Message.From.ID,
				"username", update.Message.From.UserName)
			b.reply(update.Message.Chat.ID, "You are not logged in yet.")
		}
	}

	// TODO: Handle other update types (callback queries, etc.)
}
