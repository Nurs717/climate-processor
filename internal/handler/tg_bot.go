package handler

import (
	"context"
	"fmt"
	"log"
	"strings"

	"climate-processor/internal/service"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type TGHandler struct {
	bot     *tgbotapi.BotAPI
	service *service.ClimateService
	allowed map[int64]struct{}
}

// NewTGHandler returns nil when the token is missing or invalid (the app then runs without the bot).
// Only chats listed in allowedChatIDs can read the sensor data.
func NewTGHandler(token string, allowedChatIDs []int64, svc *service.ClimateService) *TGHandler {
	if token == "" {
		log.Println("[WARN] TELEGRAM_BOT_TOKEN is not set. The bot will not be started.")
		return nil
	}

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Printf("[ERROR] Failed to initialize Telegram API: %v", err)
		return nil
	}

	allowed := make(map[int64]struct{}, len(allowedChatIDs))
	for _, id := range allowedChatIDs {
		allowed[id] = struct{}{}
	}
	if len(allowed) == 0 {
		log.Println("[WARN] TELEGRAM_ALLOWED_CHAT_IDS is empty: the bot will only tell users their chat ID.")
	}

	log.Printf("[INFO] Authorized Telegram bot account: %s", bot.Self.UserName)
	return &TGHandler{bot: bot, service: svc, allowed: allowed}
}

// Start runs the long-polling loop until ctx is cancelled.
func (h *TGHandler) Start(ctx context.Context) {
	if h == nil || h.bot == nil {
		return
	}

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30

	updates := h.bot.GetUpdatesChan(u)
	for {
		select {
		case <-ctx.Done():
			h.bot.StopReceivingUpdates()
			return
		case update, ok := <-updates:
			if !ok {
				return
			}
			h.handle(update)
		}
	}
}

// Notify sends text to every allowed chat (used for "sensor offline / back online" alerts).
func (h *TGHandler) Notify(text string) {
	if h == nil {
		return
	}
	for chatID := range h.allowed {
		h.send(chatID, text, "")
	}
}

func (h *TGHandler) handle(update tgbotapi.Update) {
	msg := update.Message
	if msg == nil || !msg.IsCommand() {
		return
	}

	// In groups a command can be addressed to another bot: /status@OtherBot.
	if at := msg.CommandWithAt(); strings.Contains(at, "@") &&
		!strings.HasSuffix(strings.ToLower(at), "@"+strings.ToLower(h.bot.Self.UserName)) {
		return
	}

	cmd := msg.Command()
	if cmd != "start" && cmd != "status" {
		return
	}

	chatID := msg.Chat.ID
	if _, ok := h.allowed[chatID]; !ok {
		h.send(chatID, fmt.Sprintf("⛔ Access denied.\nYour chat ID: %d\nAsk the owner to add it to TELEGRAM_ALLOWED_CHAT_IDS.", chatID), "")
		return
	}

	switch cmd {
	case "start":
		h.send(chatID, "Hi! Use the /status command to get the current temperature, humidity and pressure in the apartment.", "")
	case "status":
		// Request formatted text from the service layer
		h.send(chatID, h.service.GetCurrentStatus(), "Markdown")
	}
}

func (h *TGHandler) send(chatID int64, text, parseMode string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = parseMode
	if _, err := h.bot.Send(msg); err != nil {
		log.Printf("[ERROR] Telegram send to chat %d failed: %v", chatID, err)
	}
}
