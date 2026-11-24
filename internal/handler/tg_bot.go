package handler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"

	"climate-processor/internal/access"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Prefixes of the inline-button payloads ("allow:123"). Telegram limits a payload to 64 bytes.
const (
	cbAllow  = "allow"
	cbDeny   = "deny"
	cbRemove = "rm"
)

// maxNameRunes limits names taken from Telegram profiles before they are stored or shown.
const maxNameRunes = 64

// statusProvider is what the bot needs from the service layer.
type statusProvider interface {
	GetCurrentStatus() string
}

// botAPI is the part of *tgbotapi.BotAPI used for sending; tests replace it with a fake.
type botAPI interface {
	Send(c tgbotapi.Chattable) (tgbotapi.Message, error)
	Request(c tgbotapi.Chattable) (*tgbotapi.APIResponse, error)
}

type TGHandler struct {
	bot      *tgbotapi.BotAPI // used only for long polling
	api      botAPI
	username string // bot username, to ignore commands addressed to other bots in groups
	status   statusProvider
	access   *access.Store
	requests *access.Requests
}

// NewTGHandler returns nil when the token is missing or invalid (the app then runs without the bot).
// Who may read the data is decided by the access store: admins always may, everybody else has to ask
// with /start and wait for an admin to press "Allow".
func NewTGHandler(token string, store *access.Store, status statusProvider) *TGHandler {
	if token == "" {
		log.Println("[WARN] TELEGRAM_BOT_TOKEN is not set. The bot will not be started.")
		return nil
	}

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Printf("[ERROR] Failed to initialize Telegram API: %v", err)
		return nil
	}

	if len(store.Admins()) == 0 {
		log.Println("[WARN] TELEGRAM_ADMIN_CHAT_IDS is empty: nobody can approve access requests.")
	}

	log.Printf("[INFO] Authorized Telegram bot account: %s", bot.Self.UserName)
	return &TGHandler{
		bot:      bot,
		api:      bot,
		username: bot.Self.UserName,
		status:   status,
		access:   store,
		requests: access.NewRequests(),
	}
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

// Notify sends text to every chat that has access (used for "sensor offline / back online" alerts).
func (h *TGHandler) Notify(text string) {
	if h == nil {
		return
	}
	for _, chatID := range h.access.Recipients() {
		h.send(chatID, text, "")
	}
}

func (h *TGHandler) handle(update tgbotapi.Update) {
	if update.CallbackQuery != nil {
		h.handleCallback(update.CallbackQuery)
		return
	}

	msg := update.Message
	if msg == nil || !msg.IsCommand() {
		return
	}

	// In groups a command can be addressed to another bot: /status@OtherBot.
	if at := msg.CommandWithAt(); strings.Contains(at, "@") &&
		!strings.HasSuffix(strings.ToLower(at), "@"+strings.ToLower(h.username)) {
		return
	}

	chatID := msg.Chat.ID
	switch msg.Command() {
	case "start":
		h.handleStart(msg)
	case "status":
		h.handleStatus(chatID)
	case "users":
		if h.isAdminChat(msg) {
			h.handleUsers(chatID)
		}
	case "remove":
		if h.isAdminChat(msg) {
			h.handleRemove(chatID, msg.CommandArguments())
		}
	}
}

// isAdminChat is true only for a private chat with an admin: admin commands are not available in groups
// and strangers get no hint that they exist.
func (h *TGHandler) isAdminChat(msg *tgbotapi.Message) bool {
	return msg.Chat.IsPrivate() && h.access.IsAdmin(msg.Chat.ID)
}

func (h *TGHandler) handleStart(msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	if !h.access.IsAllowed(chatID) {
		h.requestAccess(msg)
		return
	}

	text := "Hi! Use the /status command to get the current temperature, humidity and pressure in the apartment."
	if h.access.IsAdmin(chatID) {
		text += "\n\nAdmin commands:\n/users - list users and remove access\n/remove <chat id> - remove a user by chat ID"
	}
	h.send(chatID, text, "")
}

func (h *TGHandler) handleStatus(chatID int64) {
	if !h.access.IsAllowed(chatID) {
		h.send(chatID, "⛔ You do not have access yet. Send /start to request it.", "")
		return
	}
	h.send(chatID, h.status.GetCurrentStatus(), "Markdown")
}

// requestAccess tells the admins that somebody wants in, with Allow / Deny buttons.
func (h *TGHandler) requestAccess(msg *tgbotapi.Message) {
	chatID := msg.Chat.ID

	admins := h.access.Admins()
	if len(admins) == 0 {
		// First run: the owner needs his own chat ID to put it into TELEGRAM_ADMIN_CHAT_IDS.
		h.send(chatID, fmt.Sprintf("⛔ Access requests are not configured on this server.\n"+
			"Your chat ID: %d\nThe owner can put it into TELEGRAM_ADMIN_CHAT_IDS to become the administrator.", chatID), "")
		return
	}

	name := displayName(msg.Chat, msg.From)
	if !h.requests.Add(access.Request{ChatID: chatID, Name: name}) {
		h.send(chatID, "⏳ Your request is already waiting for the administrator's decision.", "")
		return
	}
	h.send(chatID, "📨 Request sent to the administrator. You will get a message when it is decided.", "")

	// Plain text on purpose: names and usernames contain characters that break Markdown.
	text := fmt.Sprintf("🔔 Access request\nFrom: %s\nChat ID: %d\nChat type: %s", name, chatID, msg.Chat.Type)
	keyboard := tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("✅ Allow", fmt.Sprintf("%s:%d", cbAllow, chatID)),
		tgbotapi.NewInlineKeyboardButtonData("❌ Deny", fmt.Sprintf("%s:%d", cbDeny, chatID)),
	))
	for _, adminID := range admins {
		h.sendWithMarkup(adminID, text, keyboard)
	}
}

func (h *TGHandler) handleUsers(chatID int64) {
	text, keyboard := h.usersView()
	if keyboard == nil {
		h.send(chatID, text, "")
		return
	}
	h.sendWithMarkup(chatID, text, *keyboard)
}

func (h *TGHandler) handleRemove(chatID int64, args string) {
	id, err := strconv.ParseInt(strings.TrimSpace(args), 10, 64)
	if err != nil {
		h.send(chatID, "Usage: /remove <chat id>\nThe list with IDs is in /users.", "")
		return
	}
	h.send(chatID, h.revoke(id), "")
}

// handleCallback processes a press on an inline button. The check is done on the server side by the
// user ID of whoever pressed it, so a forwarded message or a forged payload gives a stranger nothing.
func (h *TGHandler) handleCallback(cb *tgbotapi.CallbackQuery) {
	if cb.From == nil || !h.access.IsAdmin(cb.From.ID) {
		h.answerCallback(cb.ID, "Only an administrator can do this.", true)
		return
	}

	action, rawID, found := strings.Cut(cb.Data, ":")
	id, err := strconv.ParseInt(rawID, 10, 64)
	if !found || err != nil {
		h.answerCallback(cb.ID, "Unknown button.", false)
		return
	}

	var toast string
	switch action {
	case cbAllow:
		var text string
		text, toast = h.approve(id)
		h.editMessage(cb.Message, text, nil)
	case cbDeny:
		var text string
		text, toast = h.deny(id)
		h.editMessage(cb.Message, text, nil)
	case cbRemove:
		toast = h.revoke(id)
		text, keyboard := h.usersView()
		h.editMessage(cb.Message, text, keyboard)
	default:
		toast = "Unknown button."
	}
	h.answerCallback(cb.ID, toast, false)
}

// approve gives access to a chat. It returns the new text of the admin's message ("" = leave the message
// and its buttons as they are) and a short pop-up for the admin.
func (h *TGHandler) approve(chatID int64) (text, toast string) {
	req, pending := h.requests.Take(chatID)
	name := req.Name
	if !pending {
		// For example the bot was restarted after the request was sent: the button still works.
		name = fmt.Sprintf("chat %d", chatID)
	}

	added, err := h.access.Add(chatID, name)
	if err != nil {
		log.Printf("[ERROR] Cannot save access for chat %d: %v", chatID, err)
		if pending {
			h.requests.Add(req) // keep it, so the admin can press the button again
		}
		return "", "Could not save the access list, try again."
	}
	if !added {
		return fmt.Sprintf("ℹ️ %s (%d) already has access.", name, chatID), "Already has access."
	}

	log.Printf("[INFO] Access granted to chat %d (%s)", chatID, name)
	h.send(chatID, "✅ Access granted. Use /status to see the current readings.", "")
	return fmt.Sprintf("✅ Allowed: %s (%d)", name, chatID), "Allowed."
}

func (h *TGHandler) deny(chatID int64) (text, toast string) {
	req, pending := h.requests.Take(chatID)
	if !pending {
		return fmt.Sprintf("ℹ️ The request of chat %d was already handled.", chatID), "Already handled."
	}

	log.Printf("[INFO] Access denied to chat %d (%s)", chatID, req.Name)
	h.send(chatID, "⛔ Your access request was declined.", "")
	return fmt.Sprintf("❌ Denied: %s (%d)", req.Name, chatID), "Denied."
}

// revoke removes access and tells the user about it. The returned text is meant for the admin.
func (h *TGHandler) revoke(chatID int64) string {
	switch err := h.access.Remove(chatID); {
	case errors.Is(err, access.ErrNotFound):
		return fmt.Sprintf("Chat %d is not in the list (already removed?).", chatID)
	case errors.Is(err, access.ErrProtected):
		return fmt.Sprintf("Chat %d is an administrator: remove it from TELEGRAM_ADMIN_CHAT_IDS instead.", chatID)
	case err != nil:
		log.Printf("[ERROR] Cannot remove chat %d: %v", chatID, err)
		return "Could not save the access list, try again."
	}

	log.Printf("[INFO] Access removed for chat %d", chatID)
	h.send(chatID, "⛔ Your access was revoked by the administrator.", "")
	return fmt.Sprintf("Removed chat %d.", chatID)
}

// usersView renders the list of admins and approved users. The keyboard is nil when there is
// nobody to remove (Telegram does not accept an empty keyboard).
func (h *TGHandler) usersView() (string, *tgbotapi.InlineKeyboardMarkup) {
	var b strings.Builder
	b.WriteString("👑 Admins:\n")
	for _, id := range h.access.Admins() {
		fmt.Fprintf(&b, "• %d\n", id)
	}

	users := h.access.Users()
	if len(users) == 0 {
		b.WriteString("\nNo approved users yet.")
		return b.String(), nil
	}

	b.WriteString("\n👥 Users:\n")
	rows := make([][]tgbotapi.InlineKeyboardButton, 0, len(users))
	for _, u := range users {
		fmt.Fprintf(&b, "• %s (%d), since %s\n", u.Name, u.ChatID, u.AddedAt.Format("2006-01-02"))
		label := "🗑 " + truncate(u.Name, 30)
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(label, fmt.Sprintf("%s:%d", cbRemove, u.ChatID)),
		))
	}
	b.WriteString("\nTap a button to remove access.")

	keyboard := tgbotapi.NewInlineKeyboardMarkup(rows...)
	return b.String(), &keyboard
}

// displayName builds a readable label for the admin: a group title, or "First Last (@username)".
func displayName(chat *tgbotapi.Chat, user *tgbotapi.User) string {
	if chat != nil && !chat.IsPrivate() && chat.Title != "" {
		return truncate(chat.Title, maxNameRunes)
	}
	if user == nil {
		return "unknown"
	}
	name := strings.TrimSpace(user.FirstName + " " + user.LastName)
	if user.UserName != "" {
		name = strings.TrimSpace(name + " (@" + user.UserName + ")")
	}
	if name == "" {
		name = fmt.Sprintf("user %d", user.ID)
	}
	return truncate(name, maxNameRunes)
}

func truncate(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes-1]) + "…"
}

func (h *TGHandler) send(chatID int64, text, parseMode string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = parseMode
	if _, err := h.api.Send(msg); err != nil {
		log.Printf("[ERROR] Telegram send to chat %d failed: %v", chatID, err)
	}
}

func (h *TGHandler) sendWithMarkup(chatID int64, text string, keyboard tgbotapi.InlineKeyboardMarkup) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyMarkup = keyboard
	if _, err := h.api.Send(msg); err != nil {
		log.Printf("[ERROR] Telegram send to chat %d failed: %v", chatID, err)
	}
}

// editMessage replaces the text of a bot message. A nil keyboard removes the buttons.
// An empty text means "leave the message alone".
func (h *TGHandler) editMessage(msg *tgbotapi.Message, text string, keyboard *tgbotapi.InlineKeyboardMarkup) {
	if msg == nil || text == "" {
		return
	}
	var edit tgbotapi.EditMessageTextConfig
	if keyboard != nil {
		edit = tgbotapi.NewEditMessageTextAndMarkup(msg.Chat.ID, msg.MessageID, text, *keyboard)
	} else {
		edit = tgbotapi.NewEditMessageText(msg.Chat.ID, msg.MessageID, text)
	}
	// Pressing a button twice produces the same text; Telegram answers "message is not modified", which is harmless.
	if _, err := h.api.Request(edit); err != nil && !strings.Contains(err.Error(), "message is not modified") {
		log.Printf("[ERROR] Telegram edit in chat %d failed: %v", msg.Chat.ID, err)
	}
}

// answerCallback closes the "loading" state of the pressed button and shows a short pop-up.
func (h *TGHandler) answerCallback(id, text string, alert bool) {
	cb := tgbotapi.NewCallback(id, text)
	cb.ShowAlert = alert
	if _, err := h.api.Request(cb); err != nil {
		log.Printf("[ERROR] Telegram callback answer failed: %v", err)
	}
}
