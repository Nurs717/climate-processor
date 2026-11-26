package handler

import (
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"climate-processor/internal/access"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	adminID int64 = 100
	guestID int64 = 500
)

// fakeAPI records everything the handler sends instead of calling Telegram.
type fakeAPI struct {
	sent []tgbotapi.Chattable
}

func (f *fakeAPI) Send(c tgbotapi.Chattable) (tgbotapi.Message, error) {
	f.sent = append(f.sent, c)
	return tgbotapi.Message{}, nil
}

func (f *fakeAPI) Request(c tgbotapi.Chattable) (*tgbotapi.APIResponse, error) {
	f.sent = append(f.sent, c)
	return &tgbotapi.APIResponse{Ok: true}, nil
}

// messagesTo returns the texts of the messages sent to a chat.
func (f *fakeAPI) messagesTo(chatID int64) []tgbotapi.MessageConfig {
	var out []tgbotapi.MessageConfig
	for _, c := range f.sent {
		if m, ok := c.(tgbotapi.MessageConfig); ok && m.ChatID == chatID {
			out = append(out, m)
		}
	}
	return out
}

func (f *fakeAPI) edits() []tgbotapi.EditMessageTextConfig {
	var out []tgbotapi.EditMessageTextConfig
	for _, c := range f.sent {
		if e, ok := c.(tgbotapi.EditMessageTextConfig); ok {
			out = append(out, e)
		}
	}
	return out
}

func (f *fakeAPI) callbackAnswers() []tgbotapi.CallbackConfig {
	var out []tgbotapi.CallbackConfig
	for _, c := range f.sent {
		if a, ok := c.(tgbotapi.CallbackConfig); ok {
			out = append(out, a)
		}
	}
	return out
}

type fakeStatus string

func (s fakeStatus) GetCurrentStatus() string { return string(s) }

func newTestHandler(t *testing.T, admins ...int64) (*TGHandler, *fakeAPI) {
	t.Helper()
	store, err := access.NewStore(filepath.Join(t.TempDir(), "access.json"), admins)
	if err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{}
	return &TGHandler{
		log:      slog.New(slog.DiscardHandler),
		api:      api,
		username: "ClimateBot",
		status:   fakeStatus("🌡 21.5 °C"),
		access:   store,
		requests: access.NewRequests(),
	}, api
}

// command builds an update like the one Telegram sends for a bot command typed in a chat.
func command(chatID int64, chatType, text string, from *tgbotapi.User) tgbotapi.Update {
	length := len(text)
	if i := strings.IndexByte(text, ' '); i >= 0 {
		length = i
	}
	return tgbotapi.Update{Message: &tgbotapi.Message{
		MessageID: 1,
		From:      from,
		Chat:      &tgbotapi.Chat{ID: chatID, Type: chatType},
		Text:      text,
		Entities:  []tgbotapi.MessageEntity{{Type: "bot_command", Offset: 0, Length: length}},
	}}
}

func privateCommand(chatID int64, text string) tgbotapi.Update {
	return command(chatID, "private", text, &tgbotapi.User{ID: chatID, FirstName: "Test", UserName: "test_user"})
}

// press builds an update for a button pressed by user `by` on the message `messageID` in the admin's chat.
func press(by int64, data string, messageID int) tgbotapi.Update {
	return tgbotapi.Update{CallbackQuery: &tgbotapi.CallbackQuery{
		ID:      "cb-1",
		From:    &tgbotapi.User{ID: by},
		Data:    data,
		Message: &tgbotapi.Message{MessageID: messageID, Chat: &tgbotapi.Chat{ID: by, Type: "private"}},
	}}
}

func buttonData(m tgbotapi.MessageConfig) []string {
	kb, ok := m.ReplyMarkup.(tgbotapi.InlineKeyboardMarkup)
	if !ok {
		return nil
	}
	var out []string
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			out = append(out, *b.CallbackData)
		}
	}
	return out
}

func TestStrangerRequestsAccess(t *testing.T) {
	h, api := newTestHandler(t, adminID)

	h.handle(privateCommand(guestID, "/start"))

	if got := api.messagesTo(guestID); len(got) != 1 || !strings.Contains(got[0].Text, "Request sent") {
		t.Fatalf("stranger got %+v", got)
	}
	adminMsgs := api.messagesTo(adminID)
	if len(adminMsgs) != 1 {
		t.Fatalf("admin got %d messages, want 1", len(adminMsgs))
	}
	if !strings.Contains(adminMsgs[0].Text, "Chat ID: 500") || !strings.Contains(adminMsgs[0].Text, "@test_user") {
		t.Errorf("request text lacks the details: %q", adminMsgs[0].Text)
	}
	if adminMsgs[0].ParseMode != "" {
		t.Errorf("request must be plain text (names break Markdown), got parse mode %q", adminMsgs[0].ParseMode)
	}
	if got := buttonData(adminMsgs[0]); len(got) != 2 || got[0] != "allow:500" || got[1] != "deny:500" {
		t.Errorf("buttons = %v", got)
	}
	if h.access.IsAllowed(guestID) {
		t.Error("a request alone must not give access")
	}
}

func TestRepeatedStartDoesNotRepeatTheRequest(t *testing.T) {
	h, api := newTestHandler(t, adminID)

	h.handle(privateCommand(guestID, "/start"))
	h.handle(privateCommand(guestID, "/start"))
	h.handle(privateCommand(guestID, "/start"))

	if got := len(api.messagesTo(adminID)); got != 1 {
		t.Errorf("admin got %d requests, want 1", got)
	}
	msgs := api.messagesTo(guestID)
	if last := msgs[len(msgs)-1]; !strings.Contains(last.Text, "already waiting") {
		t.Errorf("last reply to the stranger = %q", last.Text)
	}
}

func TestAllowButtonGrantsAccess(t *testing.T) {
	h, api := newTestHandler(t, adminID)
	h.handle(privateCommand(guestID, "/start"))
	api.sent = nil

	h.handle(press(adminID, "allow:500", 7))

	if !h.access.IsAllowed(guestID) {
		t.Fatal("chat must be allowed after the admin pressed Allow")
	}
	if got := api.messagesTo(guestID); len(got) != 1 || !strings.Contains(got[0].Text, "Access granted") {
		t.Errorf("the new user must be told: %+v", got)
	}
	edits := api.edits()
	if len(edits) != 1 || edits[0].MessageID != 7 || edits[0].ChatID != adminID ||
		!strings.Contains(edits[0].Text, "Allowed") || edits[0].ReplyMarkup != nil {
		t.Errorf("admin message must be replaced by the result without buttons: %+v", edits)
	}
	if got := api.callbackAnswers(); len(got) != 1 || got[0].CallbackQueryID != "cb-1" {
		t.Errorf("the button press must be answered: %+v", got)
	}

	// The name from the request is what /users will show later.
	if users := h.access.Users(); len(users) != 1 || !strings.Contains(users[0].Name, "@test_user") {
		t.Errorf("stored users = %+v", users)
	}

	// And the new user can now read the data.
	api.sent = nil
	h.handle(privateCommand(guestID, "/status"))
	if got := api.messagesTo(guestID); len(got) != 1 || got[0].Text != "🌡 21.5 °C" || got[0].ParseMode != "Markdown" {
		t.Errorf("/status reply = %+v", got)
	}
}

func TestSecondAllowPressChangesNothing(t *testing.T) {
	h, api := newTestHandler(t, adminID)
	h.handle(privateCommand(guestID, "/start"))
	h.handle(press(adminID, "allow:500", 7))
	api.sent = nil

	h.handle(press(adminID, "allow:500", 7))

	if got := api.messagesTo(guestID); len(got) != 0 {
		t.Errorf("the user must not be notified twice: %+v", got)
	}
	if edits := api.edits(); len(edits) != 1 || !strings.Contains(edits[0].Text, "already has access") {
		t.Errorf("edits = %+v", edits)
	}
}

func TestDenyButton(t *testing.T) {
	h, api := newTestHandler(t, adminID)
	h.handle(privateCommand(guestID, "/start"))
	api.sent = nil

	h.handle(press(adminID, "deny:500", 7))

	if h.access.IsAllowed(guestID) {
		t.Fatal("denied chat must not get access")
	}
	if got := api.messagesTo(guestID); len(got) != 1 || !strings.Contains(got[0].Text, "declined") {
		t.Errorf("the user must be told: %+v", got)
	}

	// A denied person may ask again.
	api.sent = nil
	h.handle(privateCommand(guestID, "/start"))
	if got := len(api.messagesTo(adminID)); got != 1 {
		t.Errorf("a new request must reach the admin, got %d messages", got)
	}
}

func TestOnlyAdminCanPressButtons(t *testing.T) {
	h, api := newTestHandler(t, adminID)
	h.handle(privateCommand(guestID, "/start"))
	api.sent = nil

	// The stranger forges the payload of the Allow button.
	h.handle(press(guestID, "allow:500", 7))

	if h.access.IsAllowed(guestID) {
		t.Fatal("a stranger must not be able to approve himself")
	}
	answers := api.callbackAnswers()
	if len(answers) != 1 || !answers[0].ShowAlert {
		t.Errorf("the stranger must get an alert: %+v", answers)
	}
	if len(api.edits()) != 0 {
		t.Error("nothing must be edited")
	}
}

func TestMalformedButtonPayloadIsIgnored(t *testing.T) {
	h, api := newTestHandler(t, adminID)

	for _, data := range []string{"", "allow", "allow:abc", "nope:5", ":"} {
		h.handle(press(adminID, data, 1))
	}
	if len(h.access.Users()) != 0 {
		t.Error("malformed payloads must not change access")
	}
	if len(api.edits()) != 0 {
		t.Error("malformed payloads must not edit messages")
	}
}

func TestUsersListAndRemoveButton(t *testing.T) {
	h, api := newTestHandler(t, adminID)
	if _, err := h.access.Add(guestID, "Wife (@wife)"); err != nil {
		t.Fatal(err)
	}

	h.handle(privateCommand(adminID, "/users"))

	msgs := api.messagesTo(adminID)
	if len(msgs) != 1 || !strings.Contains(msgs[0].Text, "Wife (@wife) (500)") || !strings.Contains(msgs[0].Text, "100") {
		t.Fatalf("/users reply = %+v", msgs)
	}
	if got := buttonData(msgs[0]); len(got) != 1 || got[0] != "rm:500" {
		t.Fatalf("buttons = %v", got)
	}

	api.sent = nil
	h.handle(press(adminID, "rm:500", 9))

	if h.access.IsAllowed(guestID) {
		t.Fatal("chat must lose access")
	}
	if got := api.messagesTo(guestID); len(got) != 1 || !strings.Contains(got[0].Text, "revoked") {
		t.Errorf("the user must be told: %+v", got)
	}
	edits := api.edits()
	if len(edits) != 1 || !strings.Contains(edits[0].Text, "No approved users yet") || edits[0].ReplyMarkup != nil {
		t.Errorf("the list must be re-rendered without buttons: %+v", edits)
	}

	// After removal the person can ask again, and /status says so.
	api.sent = nil
	h.handle(privateCommand(guestID, "/status"))
	if got := api.messagesTo(guestID); len(got) != 1 || !strings.Contains(got[0].Text, "/start") {
		t.Errorf("/status after removal = %+v", got)
	}
}

func TestRemoveCommand(t *testing.T) {
	h, api := newTestHandler(t, adminID)
	if _, err := h.access.Add(guestID, "Wife"); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		text string
		want string
	}{
		{"/remove", "Usage"},
		{"/remove abc", "Usage"},
		{"/remove 999", "not in the list"},
		{"/remove 100", "administrator"},
		{"/remove 500", "Removed chat 500"},
	}
	for _, tt := range tests {
		api.sent = nil
		h.handle(privateCommand(adminID, tt.text))
		got := api.messagesTo(adminID)
		if len(got) != 1 || !strings.Contains(got[0].Text, tt.want) {
			t.Errorf("%q: reply = %+v, want it to contain %q", tt.text, got, tt.want)
		}
	}
	if h.access.IsAllowed(guestID) {
		t.Error("chat 500 must be removed")
	}
	if !h.access.IsAdmin(adminID) {
		t.Error("the admin must stay")
	}
}

func TestAdminCommandsAreSilentForOthers(t *testing.T) {
	h, api := newTestHandler(t, adminID)
	if _, err := h.access.Add(guestID, "Wife"); err != nil {
		t.Fatal(err)
	}

	// An approved user (not an admin) and an admin writing from a group both get nothing.
	h.handle(privateCommand(guestID, "/users"))
	h.handle(privateCommand(guestID, "/remove 500"))
	h.handle(command(-1001, "supergroup", "/users", &tgbotapi.User{ID: adminID}))

	if len(api.sent) != 0 {
		t.Errorf("nothing must be sent, got %+v", api.sent)
	}
	if !h.access.IsAllowed(guestID) {
		t.Error("a non-admin must not be able to remove anybody")
	}
}

func TestStatusRequiresAccess(t *testing.T) {
	h, api := newTestHandler(t, adminID)

	h.handle(privateCommand(guestID, "/status"))
	got := api.messagesTo(guestID)
	if len(got) != 1 || strings.Contains(got[0].Text, "21.5") || !strings.Contains(got[0].Text, "/start") {
		t.Errorf("stranger /status = %+v", got)
	}

	api.sent = nil
	h.handle(privateCommand(adminID, "/status"))
	if got := api.messagesTo(adminID); len(got) != 1 || got[0].Text != "🌡 21.5 °C" {
		t.Errorf("admin /status = %+v", got)
	}
}

func TestGroupRequestShowsTitleAndAllowsTheGroup(t *testing.T) {
	h, api := newTestHandler(t, adminID)
	update := command(-1001, "group", "/start", &tgbotapi.User{ID: guestID, FirstName: "Wife"})
	update.Message.Chat.Title = "Family"

	h.handle(update)

	msgs := api.messagesTo(adminID)
	if len(msgs) != 1 || !strings.Contains(msgs[0].Text, "From: Family") || !strings.Contains(msgs[0].Text, "Chat ID: -1001") {
		t.Fatalf("request = %+v", msgs)
	}
	if got := buttonData(msgs[0]); len(got) != 2 || got[0] != "allow:-1001" {
		t.Errorf("buttons = %v", got)
	}

	h.handle(press(adminID, "allow:-1001", 3))
	if !h.access.IsAllowed(-1001) {
		t.Error("the whole group must get access")
	}
}

func TestCommandForAnotherBotIsIgnored(t *testing.T) {
	h, api := newTestHandler(t, adminID)

	h.handle(command(adminID, "private", "/status@SomeOtherBot", &tgbotapi.User{ID: adminID}))
	if len(api.sent) != 0 {
		t.Errorf("a command for another bot must be ignored: %+v", api.sent)
	}

	h.handle(command(adminID, "private", "/status@climatebot", &tgbotapi.User{ID: adminID}))
	if len(api.messagesTo(adminID)) != 1 {
		t.Error("a command addressed to this bot (any letter case) must work")
	}
}

func TestNoAdminsConfigured(t *testing.T) {
	h, api := newTestHandler(t)

	h.handle(privateCommand(guestID, "/start"))

	got := api.messagesTo(guestID)
	if len(got) != 1 || !strings.Contains(got[0].Text, "not configured") || !strings.Contains(got[0].Text, "Your chat ID: 500") {
		t.Errorf("reply = %+v", got)
	}
	if _, pending := h.requests.Take(guestID); pending {
		t.Error("nothing must be queued when there is nobody to decide")
	}
}

func TestNotifyReachesEveryoneWithAccess(t *testing.T) {
	h, api := newTestHandler(t, adminID)
	if _, err := h.access.Add(guestID, "Wife"); err != nil {
		t.Fatal(err)
	}

	h.Notify("sensor is silent")

	for _, id := range []int64{adminID, guestID} {
		if got := api.messagesTo(id); len(got) != 1 || got[0].Text != "sensor is silent" {
			t.Errorf("chat %d got %+v", id, got)
		}
	}
}

func TestDisplayName(t *testing.T) {
	private := &tgbotapi.Chat{ID: 1, Type: "private"}
	tests := []struct {
		name string
		chat *tgbotapi.Chat
		user *tgbotapi.User
		want string
	}{
		{"full profile", private, &tgbotapi.User{FirstName: "Ann", LastName: "Lee", UserName: "ann_lee"}, "Ann Lee (@ann_lee)"},
		{"first name only", private, &tgbotapi.User{FirstName: "Ann"}, "Ann"},
		{"username only", private, &tgbotapi.User{UserName: "ann"}, "(@ann)"},
		{"no name at all", private, &tgbotapi.User{ID: 42}, "user 42"},
		{"group title wins", &tgbotapi.Chat{Type: "group", Title: "Family"}, &tgbotapi.User{FirstName: "Ann"}, "Family"},
		{"no user", private, nil, "unknown"},
		{"long name is cut", private, &tgbotapi.User{FirstName: strings.Repeat("я", 100)}, strings.Repeat("я", maxNameRunes-1) + "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := displayName(tt.chat, tt.user); got != tt.want {
				t.Errorf("displayName = %q, want %q", got, tt.want)
			}
		})
	}
}
