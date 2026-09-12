package handler

// ===================================================
// Telegram Channel Publishing Manager
// Single-file Vercel-compatible implementation
// No database, no external persistence.
// ===================================================

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ===================================================
// Telegram Models
// ===================================================

type Update struct {
	UpdateID      int            `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

type User struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

type Message struct {
	MessageID      int         `json:"message_id"`
	Chat           Chat        `json:"chat"`
	From           *User       `json:"from"`
	Text           string      `json:"text"`
	Photo          []PhotoSize `json:"photo"`
	Video          *Video      `json:"video"`
	Caption        string      `json:"caption"`
	MediaGroupID   string      `json:"media_group_id"`
	ReplyToMessage *Message    `json:"reply_to_message"`
}

type PhotoSize struct {
	FileID string `json:"file_id"`
}

type Video struct {
	FileID string `json:"file_id"`
}

type Chat struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	Type  string `json:"type"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	Data    string   `json:"data"`
	Message *Message `json:"message"`
	From    User     `json:"from"`
}

// ===================================================
// Internal Models
// ===================================================

type MediaItem struct {
	Type   string `json:"type"`
	FileID string `json:"file_id"`
}

type InputMedia struct {
	Type    string `json:"type"`
	Media   string `json:"media"`
	Caption string `json:"caption,omitempty"`
}

type InlineButton struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

type Channel struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type Draft struct {
	Media        []MediaItem
	MediaGroupID string
	Caption      string
	Buttons      []InlineButton
}

type PublishResult struct {
	ChannelID string
	Title     string
	Success   bool
	Err       string
}

// State constants (avoid typos)
const (
	StateIdle             = ""
	StateAwaitCaption     = "await_caption"
	StateAwaitChannelID   = "await_channel_id"
	StateAwaitButtonName  = "await_button_name"
	StateAwaitButtonURL   = "await_button_url"
	StateAwaitEditCaption = "await_edit_caption"
	StateAwaitText        = "await_text"
	StateAwaitTranslation = "await_translation"
)

type UserSession struct {
	UserID            int64
	Draft             *Draft
	State             string
	Channels          []Channel
	SelectedChannels  map[string]bool
	SelectedLanguage  string
	LastBotMessageID  int
	LastMenuMessageID int
	PendingButtonName string
	// duplicate guard for callbacks
	Guard map[string]time.Time
}

// ===================================================
// In-memory state (Vercel warm instance only)
// ===================================================

var (
	sessionsMu sync.RWMutex
	sessions   = make(map[int64]*UserSession)
	httpClient = &http.Client{Timeout: 12 * time.Second)
)

func getSession(userID int64) *UserSession {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	s := sessions[userID]
	if s == nil {
		s = &UserSession{
			UserID:           userID,
			SelectedChannels: make(map[string]bool),
			SelectedLanguage: "ar",
			Guard:            make(map[string]time.Time),
		}
		sessions[userID] = s
	}
	return s
}

// guardCallback prevents the same callback from being executed twice
// within a short window. Returns true if it should be blocked.
func guardCallback(s *UserSession, cqID string) bool {
	if cqID == "" {
		return false
	}
	now := time.Now()
	if t, ok := s.Guard[cqID]; ok && now.Sub(t) < 5*time.Second {
		return true
	}
	s.Guard[cqID] = now
	// lightweight cleanup
	if len(s.Guard) > 32 {
		for k, v := range s.Guard {
			if now.Sub(v) > 10*time.Second {
				delete(s.Guard, k)
			}
		}
	}
	return false
}

// ===================================================
// Central Telegram API helper
// ===================================================

type tgEnvelope struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
}

func callTelegramAPI(method string, payload interface{}) (json.RawMessage, error) {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		return nil, errors.New("TELEGRAM_BOT_TOKEN is not set")
	}
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/%s", token, method)

	var bodyBytes []byte
	var err error
	if payload != nil {
		bodyBytes, err = json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("%s: marshal: %w", method, err)
		}
	} else {
		bodyBytes = []byte("{}")
	}

	req, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("%s: new request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: http: %w", method, err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%s: read: %w", method, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d: %s", method, resp.StatusCode, truncate(string(respBytes), 300))
	}

	var env tgEnvelope
	if err := json.Unmarshal(respBytes, &env); err != nil {
		return nil, fmt.Errorf("%s: bad json: %w", method, err)
	}
	if !env.OK {
		return nil, fmt.Errorf("%s: telegram error: %s", method, env.Description)
	}
	return env.Result, nil
}

// ===================================================
// Telegram action wrappers
// ===================================================

func sendMessage(chatID int64, text string, keyboard [][]map[string]interface{}) int {
	payload := map[string]interface{}{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "HTML",
		"disable_web_page_preview": true,
	}
	if keyboard != nil {
		payload["reply_markup"] = map[string]interface{}{"inline_keyboard": keyboard}
	}
	raw, err := callTelegramAPI("sendMessage", payload)
	if err != nil {
		log.Printf("sendMessage: %v", err)
		return 0
	}
	var res struct {
		MessageID int `json:"message_id"`
	}
	_ = json.Unmarshal(raw, &res)
	return res.MessageID
}

func editMessageText(chatID int64, messageID int, text string, keyboard [][]map[string]interface{}) {
	payload := map[string]interface{}{
		"chat_id":    chatID,
		"message_id": messageID,
		"text":       text,
		"parse_mode": "HTML",
		"disable_web_page_preview": true,
	}
	if keyboard != nil {
		payload["reply_markup"] = map[string]interface{}{"inline_keyboard": keyboard}
	}
	if _, err := callTelegramAPI("editMessageText", payload); err != nil {
		log.Printf("editMessageText: %v", err)
	}
}

func deleteMessage(chatID int64, messageID int) {
	if messageID == 0 {
		return
	}
	_, err := callTelegramAPI("deleteMessage", map[string]interface{}{
		"chat_id":    chatID,
		"message_id": messageID,
	})
	if err != nil {
		log.Printf("deleteMessage: %v", err)
	}
}

func answerCallback(cqID, text string, alert bool) {
	payload := map[string]interface{}{
		"callback_query_id": cqID,
		"show_alert":        alert,
	}
	if text != "" {
		runes := []rune(text)
		if len(runes) > 200 {
			text = string(runes[:197]) + "..."
		}
		payload["text"] = text
	}
	if _, err := callTelegramAPI("answerCallbackQuery", payload); err != nil {
		log.Printf("answerCallbackQuery: %v", err)
	}
}

func getChat(channelID string) (*Chat, error) {
	raw, err := callTelegramAPI("getChat", map[string]interface{}{"chat_id": channelID})
	if err != nil {
		return nil, err
	}
	var c Chat
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// ===================================================
// Webhook entrypoint
// ===================================================

func Handler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if os.Getenv("TELEGRAM_BOT_TOKEN") == "" {
		log.Printf("TELEGRAM_BOT_TOKEN missing")
		http.Error(w, "server misconfigured", http.StatusInternalServerError)
		return
	}

	var update Update
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Always reply 200 quickly to Telegram
	defer w.WriteHeader(http.StatusOK)

	if update.CallbackQuery != nil {
		handleCallback(update.CallbackQuery)
		return
	}
	if update.Message != nil {
		handleMessage(update.Message)
		return
	}
}

// ===================================================
// Message handling
// ===================================================

func handleMessage(m *Message) {
	userID := m.Chat.ID
	if m.From != nil {
		userID = m.From.ID
	}
	s := getSession(userID)
	text := strings.TrimSpace(m.Text)

	// Commands
	if strings.HasPrefix(text, "/") {
		switch strings.Fields(text)[0] {
		case "/start", "/menu":
			showMainMenu(userID, s)
			return
		case "/help":
			showHelp(userID)
			return
		case "/cancel":
			clearDraft(s)
			s.State = StateIdle
			sendMessage(userID, "❌ تم إلغاء العملية والعودة للقائمة.", nil)
			showMainMenu(userID, s)
			return
		case "/channels":
			showChannelsMenu(userID, s)
			return
		case "/publish":
			s.State = StateAwaitText
			sendMessage(userID, "📤 أرسل المحتوى الذي تريد نشره (نص / صورة / فيديو / ألبوم).", nil)
			return
		case "/languages":
			showLanguages(userID)
			return
		case "/settings":
			showSettings(userID, s)
			return
		}
	}

	// State-driven text input
	switch s.State {
	case StateAwaitChannelID:
		handleAddChannelInput(userID, s, text)
		return
	case StateAwaitEditCaption:
		if s.Draft == nil {
			s.State = StateIdle
			sendMessage(userID, "⚠️ لا يوجد منشور قيد التحرير.", nil)
			return
		}
		s.Draft.Caption = text
		s.State = StateIdle
		showPreview(userID, s)
		return
	case StateAwaitButtonName:
		s.PendingButtonName = cleanString(text)
		if s.PendingButtonName == "" {
			sendMessage(userID, "⚠️ اسم الزر فارغ، أعد الإرسال.", nil)
			return
		}
		s.State = StateAwaitButtonURL
		sendMessage(userID, "🔗 أرسل رابط الزر (يبدأ بـ http:// أو https://).", nil)
		return
	case StateAwaitButtonURL:
		link := cleanString(text)
		if !strings.HasPrefix(link, "http://") && !strings.HasPrefix(link, "https://") {
			sendMessage(userID, "⚠️ الرابط غير صالح. يجب أن يبدأ بـ http:// أو https://", nil)
			return
		}
		if s.Draft == nil {
			s.Draft = &Draft{}
		}
		s.Draft.Buttons = append(s.Draft.Buttons, InlineButton{
			Text: s.PendingButtonName,
			URL:  link,
		})
		s.PendingButtonName = ""
		s.State = StateIdle
		showPreview(userID, s)
		return
	}

	// Media handling (photo / video / media group)
	if len(m.Photo) > 0 || m.Video != nil {
		handleIncomingMedia(userID, s, m)
		return
	}

	// Text content (possibly caption from a previous media)
	if text != "" {
		handleIncomingText(userID, s, m)
		return
	}
}

// ===================================================
// Incoming media
// ===================================================

func handleIncomingMedia(userID int64, s *UserSession, m *Message) {
	var item *MediaItem
	if len(m.Photo) > 0 {
		best := m.Photo[len(m.Photo)-1] // largest resolution
		item = &MediaItem{Type: "photo", FileID: best.FileID}
	} else if m.Video != nil {
		item = &MediaItem{Type: "video", FileID: m.Video.FileID}
	}
	if item == nil {
		return
	}

	groupID := m.MediaGroupID
	if s.Draft == nil || (groupID == "" && len(s.Draft.Media) > 0) || (groupID != "" && s.Draft.MediaGroupID != groupID) {
		clearDraft(s)
		s.Draft = &Draft{MediaGroupID: groupID}
	}
	s.Draft.Media = append(s.Draft.Media, *item)
	if m.Caption != "" && s.Draft.Caption == "" {
		s.Draft.Caption = cleanString(m.Caption)
	}

	// In webhook mode we cannot reliably wait for the full album.
	// We show the preview as soon as we have at least one item.
	showPreview(userID, s)
}

// ===================================================
// Incoming text (as content or as caption for existing media)
// ===================================================

func handleIncomingText(userID int64, s *UserSession, m *Message) {
	clean := cleanString(m.Text)
	if clean == "" {
		return
	}

	if s.Draft != nil && len(s.Draft.Media) > 0 {
		// treat as caption for pending media
		s.Draft.Caption = clean
		showPreview(userID, s)
		return
	}

	// plain text post
	if s.Draft == nil {
		s.Draft = &Draft{}
	}
	s.Draft.Caption = clean
	showPreview(userID, s)
}

// ===================================================
// Preview & keyboard
// ===================================================

func clearDraft(s *UserSession) {
	if s.LastBotMessageID != 0 {
		// لا يمكن حذف رسالة الويب هوك دائماً، لكن نحاول
		deleteMessage(s.UserID, s.LastBotMessageID)
		s.LastBotMessageID = 0
	}
	s.Draft = nil
	s.SelectedChannels = make(map[string]bool)
	s.PendingButtonName = ""
}

func showPreview(userID int64, s *UserSession) {
	if s.Draft == nil {
		sendMessage(userID, "⚠️ لا يوجد محتوى.", nil)
		return
	}
	desc := describeDraft(s.Draft)

	kb := [][]map[string]interface{}{
		{
			btn("🚀 نشر", "preview_publish", "success"),
			btn("📢 اختيار القنوات", "preview_select_channels", "primary"),
		},
		{
			btn("✏️ تعديل النص", "preview_edit_caption", "primary"),
			btn("🌍 ترجمة", "preview_translate", "primary"),
		},
		{
			btn("🔘 أزرار المنشور", "preview_buttons", "primary"),
			btn("🗑 حذف الأزرار", "preview_clear_buttons", "danger"),
		},
		{
			btn("❌ إلغاء", "preview_cancel", "danger"),
		},
	}
	msgID := sendMessage(userID, desc, kb)
	s.LastBotMessageID = msgID
}

func describeDraft(d *Draft) string {
	var b strings.Builder
	b.WriteString("👀 <b>معاينة المنشور</b>\n\n")

	switch {
	case len(d.Media) > 1:
		fmt.Fprintf(&b, "📚 <b>النوع:</b> ألبوم (%d عناصر)\n", len(d.Media))
	case len(d.Media) == 1:
		fmt.Fprintf(&b, "📎 <b>النوع:</b> %s\n", d.Media[0].Type)
	default:
		b.WriteString("📝 <b>النوع:</b> نص\n")
	}

	if d.Caption != "" {
		b.WriteString("\n📝 <b>النص:</b>\n")
		b.WriteString(htmlEscape(truncate(d.Caption, 500)))
		b.WriteString("\n")
	} else {
		b.WriteString("\n<i>(لا يوجد نص)</i>\n")
	}

	if len(d.Buttons) > 0 {
		b.WriteString("\n🔘 <b>الأزرار:</b>\n")
		for _, x := range d.Buttons {
			fmt.Fprintf(&b, "• %s → %s\n", htmlEscape(x.Text), htmlEscape(x.URL))
		}
	}
	return b.String()
}

// ===================================================
// Publishing
// ===================================================

func publishToChannels(userID int64, channels []Channel, d *Draft) []PublishResult {
	results := make([]PublishResult, 0, len(channels))
	for _, ch := range channels {
		res := PublishResult{ChannelID: ch.ID, Title: ch.Title, Success: true}
		var err error
		switch {
		case len(d.Media) > 1:
			err = publishMediaGroup(ch.ID, d)
		case len(d.Media) == 1:
			item := d.Media[0]
			if item.Type == "photo" {
				err = publishPhoto(ch.ID, item.FileID, d.Caption, d.Buttons)
			} else {
				err = publishVideo(ch.ID, item.FileID, d.Caption, d.Buttons)
			}
		default:
			err = publishText(ch.ID, d.Caption, d.Buttons)
		}
		if err != nil {
			res.Success = false
			res.Err = err.Error()
			log.Printf("publish to %s failed: %v", ch.ID, err)
		}
		results = append(results, res)
	}
	return results
}

func translateButtonKeyboard() [][]map[string]interface{} {
	return [][]map[string]interface{}{
		{btn("Translate", "translate", "primary")},
	}
}

func userButtonsToInline(b []InlineButton) [][]map[string]interface{} {
	if len(b) == 0 {
		return translateButtonKeyboard()
	}
	out := make([][]map[string]interface{}, 0, len(b)+1)
	for _, x := range b {
		out = append(out, []map[string]interface{}{
			{"text": x.Text, "url": x.URL},
		})
	}
	out = append(out, []map[string]interface{}{btn("Translate", "translate", "primary")})
	return out
}

func publishText(channelID, text string, extra []InlineButton) error {
	payload := map[string]interface{}{
		"chat_id":    channelID,
		"text":       text,
		"parse_mode": "HTML",
		"reply_markup": map[string]interface{}{
			"inline_keyboard": userButtonsToInline(extra),
		},
		"disable_web_page_preview": true,
	}
	_, err := callTelegramAPI("sendMessage", payload)
	return err
}

func publishPhoto(channelID, photoID, caption string, extra []InlineButton) error {
	payload := map[string]interface{}{
		"chat_id":      channelID,
		"photo":        photoID,
		"caption":      caption,
		"parse_mode":   "HTML",
		"reply_markup": map[string]interface{}{"inline_keyboard": userButtonsToInline(extra)},
	}
	_, err := callTelegramAPI("sendPhoto", payload)
	return err
}

func publishVideo(channelID, videoID, caption string, extra []InlineButton) error {
	payload := map[string]interface{}{
		"chat_id":      channelID,
		"video":        videoID,
		"caption":      caption,
		"parse_mode":   "HTML",
		"reply_markup": map[string]interface{}{"inline_keyboard": userButtonsToInline(extra)},
	}
	_, err := callTelegramAPI("sendVideo", payload)
	return err
}

func publishMediaGroup(channelID string, d *Draft) error {
	media := make([]InputMedia, 0, len(d.Media))
	for i, it := range d.Media {
		m := InputMedia{Type: it.Type, Media: it.FileID}
		if i == 0 && d.Caption != "" {
			m.Caption = d.Caption
		}
		media = append(media, m)
	}
	payload := map[string]interface{}{
		"chat_id": channelID,
		"media":   media,
	}
	raw, err := callTelegramAPI("sendMediaGroup", payload)
	if err != nil {
		return err
	}
	var msgs []struct {
		MessageID int `json:"message_id"`
	}
	_ = json.Unmarshal(raw, &msgs)

	// Attach inline keyboard to the first item (as reply)
	if len(msgs) > 0 {
		btnPayload := map[string]interface{}{
			"chat_id":             channelID,
			"text":                "ㅤ",
			"reply_to_message_id": msgs[0].MessageID,
			"reply_markup": map[string]interface{}{
				"inline_keyboard": userButtonsToInline(d.Buttons),
			},
		}
		if _, err := callTelegramAPI("sendMessage", btnPayload); err != nil {
			log.Printf("publishMediaGroup attach buttons: %v", err)
		}
	}
	return nil
}

// ===================================================
// Callback handling
// ===================================================

func handleCallback(cq *CallbackQuery) {
	if cq.Message == nil {
		answerCallback(cq.ID, "", false)
		return
	}
	s := getSession(cq.From.ID)
	if guardCallback(s, cq.ID) {
		answerCallback(cq.ID, "", false)
		return
	}
	data := cq.Data

	// Global translate button on a published post
	if data == "translate" {
		handleInlineTranslate(cq)
		return
	}

	chatID := cq.From.ID

	switch {
	case data == "menu":
		deleteMessage(chatID, cq.Message.MessageID)
		showMainMenu(chatID, s)
	case data == "menu_channels":
		deleteMessage(chatID, cq.Message.MessageID)
		showChannelsMenu(chatID, s)
	case data == "menu_publish":
		deleteMessage(chatID, cq.Message.MessageID)
		s.State = StateAwaitText
		sendMessage(chatID, "📤 أرسل المحتوى الذي تريد نشره (نص / صورة / فيديو / ألبوم).", nil)
	case data == "menu_help":
		deleteMessage(chatID, cq.Message.MessageID)
		showHelp(chatID)
	case data == "menu_settings":
		deleteMessage(chatID, cq.Message.MessageID)
		showSettings(chatID, s)
	case data == "menu_languages":
		deleteMessage(chatID, cq.Message.MessageID)
		showLanguages(chatID)

	// -------- channels --------
	case data == "ch_add":
		deleteMessage(chatID, cq.Message.MessageID)
		s.State = StateAwaitChannelID
		sendMessage(chatID, "➕ أرسل معرف القناة:\n• عام: <code>@channel</code>\n• خاص: <code>-100xxxxxxxxxx</code>", nil)
	case data == "ch_list":
		deleteMessage(chatID, cq.Message.MessageID)
		showChannelList(chatID, s)
	case data == "ch_refresh":
		deleteMessage(chatID, cq.Message.MessageID)
		refreshAllChannels(chatID, s)
	case data == "ch_delete_menu":
		deleteMessage(chatID, cq.Message.MessageID)
		showDeleteChannelMenu(chatID, s)
	case strings.HasPrefix(data, "ch_del_"):
		idx, _ := strconv.Atoi(strings.TrimPrefix(data, "ch_del_"))
		handleDeleteChannel(chatID, s, idx)
	case strings.HasPrefix(data, "ch_refresh_"):
		idx, _ := strconv.Atoi(strings.TrimPrefix(data, "ch_refresh_"))
		handleRefreshChannel(chatID, s, idx)

	// -------- preview actions --------
	case data == "preview_publish":
		deleteMessage(chatID, cq.Message.MessageID)
		showChannelSelector(chatID, s)
	case data == "preview_select_channels":
		deleteMessage(chatID, cq.Message.MessageID)
		showChannelSelector(chatID, s)
	case data == "preview_edit_caption":
		deleteMessage(chatID, cq.Message.MessageID)
		s.State = StateAwaitEditCaption
		sendMessage(chatID, "✏️ أرسل النص الجديد للمنشور.", nil)
	case data == "preview_translate":
		deleteMessage(chatID, cq.Message.MessageID)
		showTranslateMenu(chatID, s)
	case data == "preview_buttons":
		deleteMessage(chatID, cq.Message.MessageID)
		s.State = StateAwaitButtonName
		sendMessage(chatID, "🔘 أرسل اسم الزر (مثال: 🛒 شراء الآن).", nil)
	case data == "preview_clear_buttons":
		if s.Draft != nil {
			s.Draft.Buttons = nil
		}
		deleteMessage(chatID, cq.Message.MessageID)
		showPreview(chatID, s)
	case data == "preview_cancel":
		clearDraft(s)
		deleteMessage(chatID, cq.Message.MessageID)
		sendMessage(chatID, "❌ تم إلغاء المنشور.", nil)
		showMainMenu(chatID, s)

	// -------- channel toggle / publish --------
	case strings.HasPrefix(data, "sel_"):
		idx, _ := strconv.Atoi(strings.TrimPrefix(data, "sel_"))
		toggleChannelSelection(chatID, s, idx)
	case data == "pub_all":
		for i := range s.Channels {
			s.SelectedChannels[s.Channels[i].ID] = true
		}
		refreshChannelSelectorMessage(chatID, s, cq.Message.MessageID)
	case data == "pub_none":
		s.SelectedChannels = make(map[string]bool)
		refreshChannelSelectorMessage(chatID, s, cq.Message.MessageID)
	case data == "pub_confirm":
		handleConfirmPublish(chatID, s, cq)

	// -------- language selection --------
	case strings.HasPrefix(data, "lang_"):
		lang := strings.TrimPrefix(data, "lang_")
		s.SelectedLanguage = lang
		deleteMessage(chatID, cq.Message.MessageID)
		sendMessage(chatID, fmt.Sprintf("✅ تم اختيار اللغة: <b>%s</b>", langLabel(lang)), nil)
		// If there's a pending translation target for preview, apply
		if s.Draft != nil && s.Draft.Caption != "" {
			go func() { // complete within request lifecycle; do sync actually
			}()
			translated := translateTo(s.Draft.Caption, lang)
			if !strings.HasPrefix(translated, "تعذرت") && !strings.HasPrefix(translated, "لا يوجد") {
				s.Draft.Caption = translated
			}
			showPreview(chatID, s)
		}
	}

	answerCallback(cq.ID, "", false)
}

// ===================================================
// Channel management
// ===================================================

func handleAddChannelInput(userID int64, s *UserSession, text string) {
	input := cleanString(text)
	if !strings.HasPrefix(input, "@") && !strings.HasPrefix(input, "-100") {
		sendMessage(userID, "⚠️ صيغة غير صحيحة. أرسل @username أو -100xxxxxxxxxx", nil)
		return
	}
	// duplicate protection
	for _, c := range s.Channels {
		if strings.EqualFold(c.ID, input) {
			sendMessage(userID, "⚠️ هذه القناة مضافة بالفعل.", nil)
			s.State = StateIdle
			showChannelsMenu(userID, s)
			return
		}
	}
	chat, err := getChat(input)
	if err != nil {
		log.Printf("getChat(%s): %v", input, err)
		sendMessage(userID, "⚠️ تعذر الوصول للقناة. تأكد أن البوت مشرف وأن المعرّف صحيح.", nil)
		s.State = StateIdle
		showChannelsMenu(userID, s)
		return
	}
	title := chat.Title
	if title == "" {
		title = input
	}
	s.Channels = append(s.Channels, Channel{ID: input, Title: title})
	s.State = StateIdle
	sendMessage(userID,
		fmt.Sprintf("✅ تمت إضافة القناة:\n📢 <b>%s</b>\nID: <code>%s</code>",
			htmlEscape(title), htmlEscape(input)), nil)
	showChannelsMenu(userID, s)
}

func handleDeleteChannel(userID int64, s *UserSession, idx int) {
	if idx < 0 || idx >= len(s.Channels) {
		sendMessage(userID, "⚠️ قناة غير موجودة.", nil)
		return
	}
	removed := s.Channels[idx]
	s.Channels = append(s.Channels[:idx], s.Channels[idx+1:]...)
	delete(s.SelectedChannels, removed.ID)
	sendMessage(userID, fmt.Sprintf("🗑 تم حذف القناة: <b>%s</b>", htmlEscape(removed.Title)), nil)
	showDeleteChannelMenu(userID, s)
}

func handleRefreshChannel(userID int64, s *UserSession, idx int) {
	if idx < 0 || idx >= len(s.Channels) {
		return
	}
	ch := s.Channels[idx]
	chat, err := getChat(ch.ID)
	if err != nil {
		sendMessage(userID, fmt.Sprintf("❌ تعذر تحديث <b>%s</b>: %s",
			htmlEscape(ch.Title), htmlEscape(err.Error())), nil)
		return
	}
	if chat.Title != "" {
		s.Channels[idx].Title = chat.Title
	}
	sendMessage(userID, fmt.Sprintf("🔄 تم التحديث: <b>%s</b>", htmlEscape(s.Channels[idx].Title)), nil)
	showChannelsMenu(userID, s)
}

func refreshAllChannels(userID int64, s *UserSession) {
	if len(s.Channels) == 0 {
		sendMessage(userID, "لا توجد قنوات.", nil)
		return
	}
	updated := 0
	for i := range s.Channels {
		chat, err := getChat(s.Channels[i].ID)
		if err == nil && chat.Title != "" {
			s.Channels[i].Title = chat.Title
			updated++
		}
	}
	sendMessage(userID, fmt.Sprintf("🔄 تم تحديث %d/%d قناة.", updated, len(s.Channels)), nil)
	showChannelsMenu(userID, s)
}

// ===================================================
// Channel selector (multi-select)
// ===================================================

func showChannelSelector(chatID int64, s *UserSession) {
	if len(s.Channels) == 0 {
		sendMessage(chatID, "⚠️ لا توجد قنوات. أضف قناة أولاً من <b>إدارة القنوات</b>.", nil)
		showMainMenu(chatID, s)
		return
	}
	if s.Draft == nil {
		sendMessage(chatID, "⚠️ انتهت الجلسة. أعد إرسال المحتوى.", nil)
		showMainMenu(chatID, s)
		return
	}
	text, kb := buildChannelSelector(s)
	msgID := sendMessage(chatID, text, kb)
	s.LastBotMessageID = msgID
}

func refreshChannelSelectorMessage(chatID int64, s *UserSession, msgID int) {
	text, kb := buildChannelSelector(s)
	editMessageText(chatID, msgID, text, kb)
}

func buildChannelSelector(s *UserSession) (string, [][]map[string]interface{}) {
	var b strings.Builder
	b.WriteString("📢 <b>اختر القنوات المستهدفة</b>\n\n")
	var kb [][]map[string]interface{}
	for i, ch := range s.Channels {
		mark := "☐"
		if s.SelectedChannels[ch.ID] {
			mark = "☑"
		}
		kb = append(kb, []map[string]interface{}{
			btn(fmt.Sprintf("%s %s", mark, ch.Title), fmt.Sprintf("sel_%d", i), "primary"),
		})
	}
	kb = append(kb,
		[]map[string]interface{}{
			btn("✅ تحديد الكل", "pub_all", "primary"),
			btn("❌ إلغاء التحديد", "pub_none", "danger"),
		},
		[]map[string]interface{}{
			btn("🚀 نشر في القنوات المحددة", "pub_confirm", "success"),
		},
		[]map[string]interface{}{
			btn("🔙 رجوع للمعاينة", "preview_publish_back", "primary"),
		},
	)
	return b.String(), kb
}

func toggleChannelSelection(chatID int64, s *UserSession, idx int) {
	if idx < 0 || idx >= len(s.Channels) {
		return
	}
	id := s.Channels[idx].ID
	if s.SelectedChannels[id] {
		delete(s.SelectedChannels, id)
	} else {
		s.SelectedChannels[id] = true
	}
}

func handleConfirmPublish(chatID int64, s *UserSession, cq *CallbackQuery) {
	if s.Draft == nil {
		answerCallback(cq.ID, "لا يوجد منشور", true)
		return
	}
	if len(s.SelectedChannels) == 0 {
		answerCallback(cq.ID, "لم تختر أي قناة!", true)
		return
	}
	targets := make([]Channel, 0, len(s.SelectedChannels))
	for _, ch := range s.Channels {
		if s.SelectedChannels[ch.ID] {
			targets = append(targets, ch)
		}
	}
	// delete selector message
	deleteMessage(chatID, cq.Message.MessageID)

	results := publishToChannels(chatID, targets, s.Draft)
	clearDraft(s)

	report := buildReport(results)
	sendMessage(chatID, report, [][]map[string]interface{}{
		{btn("🏠 القائمة الرئيسية", "menu", "primary")},
	})
}

func buildReport(results []PublishResult) string {
	ok, fail := 0, 0
	for _, r := range results {
		if r.Success {
			ok++
		} else {
			fail++
		}
	}
	var b strings.Builder
	b.WriteString("🚀 <b>اكتمل النشر</b>\n\n")
	fmt.Fprintf(&b, "✅ تم النشر: %d\n❌ فشل: %d\n\n", ok, fail)
	if fail > 0 {
		b.WriteString("<b>تفاصيل الأخطاء:</b>\n")
		for _, r := range results {
			if !r.Success {
				fmt.Fprintf(&b, "📢 %s\n<code>%s</code>\n\n",
					htmlEscape(r.Title), htmlEscape(r.Err))
			}
		}
	}
	return b.String()
}

// ===================================================
// Menus
// ===================================================

func showMainMenu(chatID int64, s *UserSession) {
	kb := [][]map[string]interface{}{
		{
			btn("📢 إدارة القنوات", "menu_channels", "primary"),
			btn("📤 نشر", "menu_publish", "success"),
		},
		{
			btn("🌍 ترجمة", "menu_languages", "primary"),
			btn("⚙️ الإعدادات", "menu_settings", "primary"),
		},
		{
			btn("❓ المساعدة", "menu_help", "primary"),
		},
	}
	msgID := sendMessage(chatID,
		"<b>لوحة التحكم الرئيسية</b>\nاختر العملية من القائمة أدناه:",
		kb)
	s.LastMenuMessageID = msgID
}

func showChannelsMenu(chatID int64, s *UserSession) {
	kb := [][]map[string]interface{}{
		{
			btn("➕ إضافة قناة", "ch_add", "success"),
			btn("📋 قنواتي", "ch_list", "primary"),
		},
		{
			btn("🗑 حذف قناة", "ch_delete_menu", "danger"),
			btn("🔄 تحديث المعلومات", "ch_refresh", "primary"),
		},
		{
			btn("🏠 القائمة الرئيسية", "menu", "primary"),
		},
	}
	sendMessage(chatID, "📢 <b>إدارة القنوات</b>", kb)
}

func showChannelList(chatID int64, s *UserSession) {
	if len(s.Channels) == 0 {
		sendMessage(chatID, "لا توجد قنوات مضافة.", nil)
		return
	}
	var b strings.Builder
	b.WriteString("📋 <b>قنواتك:</b>\n\n")
	for i, ch := range s.Channels {
		fmt.Fprintf(&b, "%d. 📢 <b>%s</b>\n   ID: <code>%s</code>\n", i+1, htmlEscape(ch.Title), htmlEscape(ch.ID))
	}
	kb := [][]map[string]interface{}{{btn("🔙 رجوع", "menu_channels", "primary")}}
	sendMessage(chatID, b.String(), kb)
}

func showDeleteChannelMenu(chatID int64, s *UserSession) {
	if len(s.Channels) == 0 {
		sendMessage(chatID, "لا توجد قنوات للحذف.", nil)
		return
	}
	var kb [][]map[string]interface{}
	for i, ch := range s.Channels {
		kb = append(kb, []map[string]interface{}{
			btn("🗑 "+ch.Title, fmt.Sprintf("ch_del_%d", i), "danger"),
		})
	}
	kb = append(kb, []map[string]interface{}{btn("🔙 رجوع", "menu_channels", "primary")})
	sendMessage(chatID, "اختر القناة التي تريد حذفها:", kb)
}

func showSettings(chatID int64, s *UserSession) {
	kb := [][]map[string]interface{}{
		{btn("🌍 لغة الترجمة", "menu_languages", "primary")},
		{btn("🏠 القائمة الرئيسية", "menu", "primary")},
	}
	sendMessage(chatID,
		fmt.Sprintf("<b>⚙️ الإعدادات</b>\n\n🌍 اللغة الحالية: <b>%s</b>", langLabel(s.SelectedLanguage)),
		kb)
}

func showLanguages(chatID int64) {
	kb := [][]map[string]interface{}{
		{btn("🇸🇦 العربية", "lang_ar", "primary"), btn("🇺🇸 English", "lang_en", "primary")},
		{btn("🇹🇷 Türkçe", "lang_tr", "primary"), btn("🇫🇷 Français", "lang_fr", "primary")},
		{btn("🇪🇸 Español", "lang_es", "primary"), btn("🇩🇪 Deutsch", "lang_de", "primary")},
		{btn("🏠 القائمة الرئيسية", "menu", "primary")},
	}
	sendMessage(chatID, "🌍 اختر لغة الترجمة:", kb)
}

func showTranslateMenu(chatID int64, s *UserSession) {
	if s.Draft == nil || (s.Draft.Caption == "" && len(s.Draft.Media) == 0) {
		sendMessage(chatID, "⚠️ لا يوجد محتوى للترجمة.", nil)
		return
	}
	kb := [][]map[string]interface{}{
		{btn("🇸🇦 العربية", "trg_ar", "primary"), btn("🇺🇸 English", "trg_en", "primary")},
		{btn("🇹🇷 Türkçe", "trg_tr", "primary"), btn("🇫🇷 Français", "trg_fr", "primary")},
		{btn("🇪🇸 Español", "trg_es", "primary"), btn("🇩🇪 Deutsch", "trg_de", "primary")},
		{btn("❌ إلغاء", "preview_publish", "danger")},
	}
	sendMessage(chatID, "🌍 اختر اللغة التي تريد الترجمة إليها:", kb)
}

// ===================================================
// Inline Translate button on published posts
// ===================================================

func handleInlineTranslate(cq *CallbackQuery) {
	var text string
	if cq.Message != nil {
		if cleanString(cq.Message.Caption) != "" {
			text = cq.Message.Caption
		} else if cleanString(cq.Message.Text) != "" {
			text = cq.Message.Text
		} else if cq.Message.ReplyToMessage != nil {
			if cleanString(cq.Message.ReplyToMessage.Caption) != "" {
				text = cq.Message.ReplyToMessage.Caption
			} else if cleanString(cq.Message.ReplyToMessage.Text) != "" {
				text = cq.Message.ReplyToMessage.Text
			}
		}
	}
	if text == "" {
		answerCallback(cq.ID, "لا يوجد نص للترجمة.", true)
		return
	}
	translated := translateTo(text, "ar")
	answerCallback(cq.ID, translated, true)
}

// ===================================================
// Translation
// ===================================================

func langLabel(code string) string {
	switch code {
	case "ar":
		return "العربية"
	case "en":
		return "English"
	case "tr":
		return "Türkçe"
	case "fr":
		return "Français"
	case "es":
		return "Español"
	case "de":
		return "Deutsch"
	}
	return code
}

func translateTo(text, target string) string {
	clean := cleanString(text)
	if clean == "" {
		return "لا يوجد نص محدد للترجمة."
	}
	runes := []rune(clean)
	if len(runes) > 490 {
		clean = string(runes[:490])
	}

	baseURL := os.Getenv("TRANSLATION_API_URL")
	if baseURL == "" {
		baseURL = "https://api.mymemory.translated.net/get"
	}
	apiKey := os.Getenv("TRANSLATION_API_KEY")

	// We assume source is not the same as target
	src := "en"
	if target == "en" {
		src = "ar"
	}

	q := url.Values{}
	q.Set("q", clean)
	q.Set("langpair", src+"|"+target)
	if apiKey != "" {
		q.Set("key", apiKey)
	}
	full := baseURL + "?" + q.Encode()

	req, err := http.NewRequest(http.MethodGet, full, nil)
	if err != nil {
		log.Printf("translateTo: new request: %v", err)
		return "تعذرت الترجمة حالياً."
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		log.Printf("translateTo: http: %v", err)
		return "تعذرت الترجمة حالياً."
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("translateTo: read: %v", err)
		return "تعذرت الترجمة حالياً."
	}
	if resp.StatusCode != http.StatusOK {
		log.Printf("translateTo: status %d: %s", resp.StatusCode, truncate(string(body), 200))
		return "تعذرت الترجمة حالياً."
	}

	var result struct {
		ResponseData struct {
			TranslatedText string `json:"translatedText"`
		} `json:"responseData"`
		ResponseStatus interface{} `json:"responseStatus"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("translateTo: json: %v", err)
		return "تعذرت ترجمة النص."
	}
	ok := false
	switch v := result.ResponseStatus.(type) {
	case float64:
		ok = v == 200
	case string:
		ok = v == "200"
	}
	if !ok {
		log.Printf("translateTo: non-200 status: %s", truncate(string(body), 200))
		return "تعذرت الترجمة (قد يكون الحد اليومي مستنفداً)."
	}
	if result.ResponseData.TranslatedText == "" {
		return "تعذرت ترجمة النص."
	}
	return result.ResponseData.TranslatedText
}

// Keep the old entry point working (used by inline Translate button)
func translateToArabic(text string) string { return translateTo(text, "ar") }

// ===================================================
// Utilities
// ===================================================

func btn(text, data, style string) map[string]interface{} {
	b := map[string]interface{}{
		"text":          text,
		"callback_data": data,
	}
	if style != "" {
		b["style"] = style
	}
	return b
}

func cleanString(s string) string {
	s = strings.TrimSpace(s)
	// zero-width / invisible chars
	replacer := strings.NewReplacer(
		"\u200b", "", // zero width space
		"\u200c", "", // ZWNJ
		"\u200d", "", // ZWJ
		"\u2060", "", // word joiner
		"\ufeff", "", // BOM
		"\u2800", "", // braille blank
		"ㅤ",     "", // hangul filler
	)
	s = replacer.Replace(s)
	// collapse multiple blank lines
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	// collapse spaces
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return strings.TrimSpace(s)
}

func htmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
	)
	return r.Replace(s)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

// SortChannels - optional helper if you want alphabetical ordering
func sortChannels(chs []Channel) {
	sort.Slice(chs, func(i, j int) bool { return chs[i].Title < chs[j].Title })
}
