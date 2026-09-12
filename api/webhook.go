package handler

// ===================================================
// Telegram Channel Publishing Manager
// Single-file Vercel Serverless implementation
// Fixes: translate button, album single-message, UX
// Adds: inline query mode (translate to many langs)
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
	InlineQuery   *InlineQuery   `json:"inline_query"`
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

type InlineQuery struct {
	ID     string `json:"id"`
	From   User   `json:"from"`
	Query  string `json:"query"`
	Offset string `json:"offset"`
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

// State constants
const (
	StateIdle             = ""
	StateAwaitChannelID   = "await_channel_id"
	StateAwaitButtonName  = "await_button_name"
	StateAwaitButtonURL   = "await_button_url"
	StateAwaitEditCaption = "await_edit_caption"
	StateAwaitText        = "await_text"
)

type UserSession struct {
	UserID            int64
	Draft             *Draft
	State             string
	Channels          []Channel
	SelectedChannels  map[string]bool
	SelectedLanguage  string
	LastBotMessageID  int
	LastPreviewMsgID  int
	LastMediaTime     time.Time
	PendingButtonName string
	Guard             map[string]time.Time
}

// ===================================================
// In-memory state
// NOTE: on Vercel cold start this is lost. See notes.
// ===================================================

var (
	sessionsMu sync.RWMutex
	sessions   = make(map[int64]*UserSession)
	httpClient = &http.Client{Timeout: 12 * time.Second}
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

func guardCallback(s *UserSession, cqID string) bool {
	if cqID == "" {
		return false
	}
	now := time.Now()
	if t, ok := s.Guard[cqID]; ok && now.Sub(t) < 5*time.Second {
		return true
	}
	s.Guard[cqID] = now
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
// Action wrappers
// ===================================================

func sendMessage(chatID int64, text string, keyboard [][]map[string]interface{}) int {
	payload := map[string]interface{}{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "HTML",
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
		"chat_id":                  chatID,
		"message_id":               messageID,
		"text":                     text,
		"parse_mode":               "HTML",
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
	if r.Method == http.MethodGet {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return
	}
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

	w.WriteHeader(http.StatusOK)

	if update.InlineQuery != nil {
		handleInlineQuery(update.InlineQuery)
		return
	}
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
		cmd := strings.Fields(text)[0]
		if idx := strings.Index(cmd, "@"); idx != -1 {
			cmd = cmd[:idx]
		}
		switch cmd {
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
		s.Draft.Caption = cleanString(text)
		s.State = StateIdle
		updatePreview(userID, s)
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
		updatePreview(userID, s)
		return
	}

	// Media
	if len(m.Photo) > 0 || m.Video != nil {
		handleIncomingMedia(userID, s, m)
		return
	}

	// Text content
	if text != "" {
		handleIncomingText(userID, s, m)
		return
	}
}

// ===================================================
// Media handling — ONE preview message, edit-in-place
// ===================================================

func handleIncomingMedia(userID int64, s *UserSession, m *Message) {
	var item *MediaItem
	if len(m.Photo) > 0 {
		best := m.Photo[len(m.Photo)-1]
		item = &MediaItem{Type: "photo", FileID: best.FileID}
	} else if m.Video != nil {
		item = &MediaItem{Type: "video", FileID: m.Video.FileID}
	}
	if item == nil {
		return
	}

	groupID := m.MediaGroupID
	now := time.Now()

	// Decide if this belongs to a new draft
	newDraft := false
	if s.Draft == nil {
		newDraft = true
	} else if groupID == "" && !s.LastMediaTime.IsZero() && now.Sub(s.LastMediaTime) > 3*time.Second {
		// old single-media flow timed out → new draft
		newDraft = true
	} else if groupID != "" && s.Draft.MediaGroupID != "" && s.Draft.MediaGroupID != groupID {
		newDraft = true
	}

	if newDraft {
		clearDraft(s)
		s.Draft = &Draft{MediaGroupID: groupID}
	}
	if s.Draft.MediaGroupID == "" && groupID != "" {
		s.Draft.MediaGroupID = groupID
	}
	s.Draft.Media = append(s.Draft.Media, *item)
	if m.Caption != "" && s.Draft.Caption == "" {
		s.Draft.Caption = cleanString(m.Caption)
	}
	s.LastMediaTime = now

	// If we already sent a preview message → EDIT it. Otherwise send once.
	if s.LastPreviewMsgID != 0 {
		updatePreview(userID, s)
	} else {
		showPreview(userID, s)
	}
}

func handleIncomingText(userID int64, s *UserSession, m *Message) {
	clean := cleanString(m.Text)
	if clean == "" {
		return
	}
	if s.Draft != nil && len(s.Draft.Media) > 0 {
		s.Draft.Caption = clean
		updatePreview(userID, s)
		return
	}
	if s.Draft == nil {
		s.Draft = &Draft{}
	}
	s.Draft.Caption = clean
	showPreview(userID, s)
}

// ===================================================
// Preview (single message pattern)
// ===================================================

func clearDraft(s *UserSession) {
	if s.LastPreviewMsgID != 0 {
		deleteMessage(s.UserID, s.LastPreviewMsgID)
		s.LastPreviewMsgID = 0
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
	kb := previewKeyboard()
	msgID := sendMessage(userID, desc, kb)
	s.LastPreviewMsgID = msgID
}

func updatePreview(userID int64, s *UserSession) {
	if s.Draft == nil {
		return
	}
	if s.LastPreviewMsgID == 0 {
		showPreview(userID, s)
		return
	}
	desc := describeDraft(s.Draft)
	kb := previewKeyboard()
	editMessageText(userID, s.LastPreviewMsgID, desc, kb)
}

func previewKeyboard() [][]map[string]interface{} {
	return [][]map[string]interface{}{
		{
			btn("🚀 نشر", "preview_publish", "success"),
			btn("📢 اختيار القنوات", "preview_select_channels", "primary"),
		},
		{
			btn("✏️ تعديل النص", "preview_edit_caption", "primary"),
			btn("🌍 ترجمة", "preview_translate", "primary"),
		},
		{
			btn("🔘 إضافة زر", "preview_buttons", "primary"),
			btn("🗑 حذف الأزرار", "preview_clear_buttons", "danger"),
		},
		{
			btn("❌ إلغاء", "preview_cancel", "danger"),
		},
	}
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

func publishToChannels(channels []Channel, d *Draft) []PublishResult {
	results := make([]PublishResult, 0, len(channels))
	for _, ch := range channels {
		res := PublishResult{ChannelID: ch.ID, Title: ch.Title, Success: true}
		var err error
		switch {
		case len(d.Media) > 1:
			err = publishMediaGroup(ch.ID, d)
		case len(d.Media) == 1:
			it := d.Media[0]
			if it.Type == "photo" {
				err = publishPhoto(ch.ID, it.FileID, d.Caption, d.Buttons)
			} else {
				err = publishVideo(ch.ID, it.FileID, d.Caption, d.Buttons)
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
		{btn("🌐 Translate", "translate", "primary")},
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
	out = append(out, []map[string]interface{}{btn("🌐 Translate", "translate", "primary")})
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
	chatID := cq.From.ID

	// Attempt to recover draft from the callback message (session resilience)
	recoverDraftFromMessage(s, cq.Message)

	// Translate button on a published post (channel or private)
	if data == "translate" {
		handleInlineTranslate(cq)
		return
	}

	switch {
	// -------- main menu --------
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
	case data == "menu_identity":
		deleteMessage(chatID, cq.Message.MessageID)
		me, _ := getChat(fmt.Sprintf("%d", chatID))
		txt := fmt.Sprintf("🆔 <b>معرّفك:</b> <code>%d</code>", chatID)
		if me != nil && me.Title != "" {
			txt += fmt.Sprintf("\n📛 <b>الاسم:</b> %s", htmlEscape(me.Title))
		}
		sendMessage(chatID, txt, backKeyboard())

	// -------- channels --------
	case data == "ch_add":
		deleteMessage(chatID, cq.Message.MessageID)
		s.State = StateAwaitChannelID
		sendMessage(chatID,
			"➕ <b>إضافة قناة</b>\n\n"+
				"أرسل معرف القناة:\n"+
				"• عام: <code>@channel_username</code>\n"+
				"• خاص: <code>-100xxxxxxxxxx</code>\n\n"+
				"⚠️ يجب رفع البوت مشرفاً في القناة.",
			backKeyboard())
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

	// -------- preview --------
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
		updatePreview(chatID, s)
	case data == "preview_cancel":
		clearDraft(s)
		s.State = StateIdle
		deleteMessage(chatID, cq.Message.MessageID)
		sendMessage(chatID, "❌ تم إلغاء المنشور.", nil)
		showMainMenu(chatID, s)

	// -------- channel selector --------
	case strings.HasPrefix(data, "sel_"):
		idx, _ := strconv.Atoi(strings.TrimPrefix(data, "sel_"))
		toggleChannelSelection(s, idx)
		refreshChannelSelectorMessage(chatID, s, cq.Message.MessageID)
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
	case data == "pub_back":
		deleteMessage(chatID, cq.Message.MessageID)
		showPreview(chatID, s)

	// -------- languages (settings) --------
	case strings.HasPrefix(data, "lang_"):
		lang := strings.TrimPrefix(data, "lang_")
		s.SelectedLanguage = lang
		deleteMessage(chatID, cq.Message.MessageID)
		sendMessage(chatID, fmt.Sprintf("✅ تم اختيار اللغة الافتراضية: <b>%s</b>", langLabel(lang)), backKeyboard())

	// -------- translate target for preview --------
	case strings.HasPrefix(data, "trg_"):
		lang := strings.TrimPrefix(data, "trg_")
		if s.Draft == nil || s.Draft.Caption == "" {
			answerCallback(cq.ID, "لا يوجد نص للترجمة.", true)
			return
		}
		deleteMessage(chatID, cq.Message.MessageID)
		src := detectLang(s.Draft.Caption)
		translated := translateText(s.Draft.Caption, src, lang)
		if strings.HasPrefix(translated, "تعذرت") || strings.HasPrefix(translated, "لا يوجد") {
			sendMessage(chatID, "⚠️ "+translated, nil)
		} else {
			s.Draft.Caption = translated
		}
		updatePreview(chatID, s)
	}

	answerCallback(cq.ID, "", false)
}

// Recover draft from callback message when session was lost
func recoverDraftFromMessage(s *UserSession, msg *Message) {
	if s.Draft != nil {
		return
	}
	if msg == nil {
		return
	}
	if len(msg.Photo) > 0 {
		s.Draft = &Draft{
			Media: []MediaItem{{Type: "photo", FileID: msg.Photo[len(msg.Photo)-1].FileID}},
		}
		if msg.Caption != "" {
			s.Draft.Caption = cleanString(msg.Caption)
		}
	} else if msg.Video != nil {
		s.Draft = &Draft{
			Media: []MediaItem{{Type: "video", FileID: msg.Video.FileID}},
		}
		if msg.Caption != "" {
			s.Draft.Caption = cleanString(msg.Caption)
		}
	}
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
		sendMessage(userID,
			"⚠️ <b>تعذر الوصول للقناة!</b>\n\n"+
				"تأكد من:\n"+
				"• رفع البوت مشرفاً في القناة\n"+
				"• كتابة المعرّف بشكل صحيح\n"+
				"• أن القناة عامة أو أن البوت عضو فيها",
			nil)
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
		fmt.Sprintf("✅ <b>تمت إضافة القناة بنجاح</b>\n\n📢 <b>%s</b>\n🆔 <code>%s</code>",
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
// Channel selector
// ===================================================

func showChannelSelector(chatID int64, s *UserSession) {
	if len(s.Channels) == 0 {
		sendMessage(chatID,
			"⚠️ <b>لا توجد قنوات مضافة!</b>\n\n"+
				"انتقل إلى <b>إدارة القنوات</b> ثم اضغط ➕ إضافة قناة.\n\n"+
				"<i>ملاحظة: على Vercel قد تُفقد قائمة القنوات عند إعادة تشغيل السيرفر. أعد إضافتها عند الحاجة.</i>",
			[][]map[string]interface{}{
				{btn("📢 إدارة القنوات", "menu_channels", "primary")},
				{btn("🏠 القائمة الرئيسية", "menu", "primary")},
			})
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
	b.WriteString("يمكنك اختيار أكثر من قناة، ثم اضغط 🚀 نشر.\n\n")

	var kb [][]map[string]interface{}

	// Two channels per row for compactness
	var row []map[string]interface{}
	for i, ch := range s.Channels {
		mark := "☐"
		if s.SelectedChannels[ch.ID] {
			mark = "☑"
		}
		label := fmt.Sprintf("%s %s", mark, truncate(ch.Title, 20))
		row = append(row, btn(label, fmt.Sprintf("sel_%d", i), "primary"))
		if len(row) == 2 {
			kb = append(kb, row)
			row = nil
		}
	}
	if len(row) > 0 {
		kb = append(kb, row)
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
			btn("🔙 رجوع للمعاينة", "pub_back", "primary"),
		},
	)
	return b.String(), kb
}

func toggleChannelSelection(s *UserSession, idx int) {
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
	deleteMessage(chatID, cq.Message.MessageID)

	results := publishToChannels(targets, s.Draft)
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
		b.WriteString("<b>تفاصيل الأخطاء:</b>\n\n")
		for _, r := range results {
			if !r.Success {
				fmt.Fprintf(&b, "📢 <b>%s</b>\n<code>%s</code>\n\n",
					htmlEscape(r.Title), htmlEscape(r.Err))
			}
		}
	}
	return b.String()
}

// ===================================================
// Menus
// ===================================================

func backKeyboard() [][]map[string]interface{} {
	return [][]map[string]interface{}{
		{btn("🔙 رجوع", "menu", "primary")},
	}
}

func showMainMenu(chatID int64, s *UserSession) {
	kb := [][]map[string]interface{}{
		{
			btn("📢 إدارة القنوات", "menu_channels", "primary"),
			btn("📤 نشر", "menu_publish", "success"),
		},
		{
			btn("🌍 الترجمة", "menu_languages", "primary"),
			btn("⚙️ الإعدادات", "menu_settings", "primary"),
		},
		{
			btn("🆔 معرّفي", "menu_identity", "primary"),
			btn("❓ المساعدة", "menu_help", "primary"),
		},
	}
	sendMessage(chatID,
		"<b>🎛️ لوحة التحكم الرئيسية</b>\n\n"+
			"اختر العملية من القائمة أدناه:",
		kb)
}

func showHelp(chatID int64) {
	helpText := "<b>❓ المساعدة</b>\n\n" +
		"<b>📋 الأوامر:</b>\n" +
		"• /start — لوحة التحكم\n" +
		"• /menu — القائمة الرئيسية\n" +
		"• /channels — إدارة القنوات\n" +
		"• /publish — بدء النشر\n" +
		"• /languages — لغات الترجمة\n" +
		"• /settings — الإعدادات\n" +
		"• /cancel — إلغاء العملية\n\n" +
		"<b>📌 طريقة الاستخدام:</b>\n" +
		"1️⃣ أضف البوت مشرفاً في قناتك\n" +
		"2️⃣ أضف القناة من \"إدارة القنوات\"\n" +
		"3️⃣ أرسل المحتوى (نص/صورة/فيديو/ألبوم)\n" +
		"4️⃣ اختر القنوات ثم اضغط 🚀 نشر\n\n" +
		"<b>🌐 Inline Mode:</b>\n" +
		"اكتب في أي مكان:\n" +
		"<code>@YourBotName نص للترجمة</code>\n" +
		"ستظهر لك قائمة باللغات المتاحة للترجمة."
	sendMessage(chatID, helpText, backKeyboard())
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
		{btn("🏠 القائمة الرئيسية", "menu", "primary")},
	}
	sendMessage(chatID, "📢 <b>إدارة القنوات</b>\n\nاختر العملية:", kb)
}

func showChannelList(chatID int64, s *UserSession) {
	if len(s.Channels) == 0 {
		sendMessage(chatID, "لا توجد قنوات مضافة.", backKeyboard())
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "📋 <b>قنواتك (%d):</b>\n\n", len(s.Channels))
	for i, ch := range s.Channels {
		fmt.Fprintf(&b, "%d. 📢 <b>%s</b>\n   🆔 <code>%s</code>\n\n",
			i+1, htmlEscape(ch.Title), htmlEscape(ch.ID))
	}
	sendMessage(chatID, b.String(), [][]map[string]interface{}{
		{btn("🔙 رجوع", "menu_channels", "primary")},
	})
}

func showDeleteChannelMenu(chatID int64, s *UserSession) {
	if len(s.Channels) == 0 {
		sendMessage(chatID, "لا توجد قنوات للحذف.", backKeyboard())
		return
	}
	var kb [][]map[string]interface{}
	for i, ch := range s.Channels {
		kb = append(kb, []map[string]interface{}{
			btn("🗑 "+truncate(ch.Title, 30), fmt.Sprintf("ch_del_%d", i), "danger"),
		})
	}
	kb = append(kb, []map[string]interface{}{btn("🔙 رجوع", "menu_channels", "primary")})
	sendMessage(chatID, "🗑 <b>حذف قناة</b>\n\nاختر القناة:", kb)
}

func showSettings(chatID int64, s *UserSession) {
	kb := [][]map[string]interface{}{
		{btn("🌍 لغة الترجمة الافتراضية", "menu_languages", "primary")},
		{btn("🏠 القائمة الرئيسية", "menu", "primary")},
	}
	sendMessage(chatID,
		fmt.Sprintf("<b>⚙️ الإعدادات</b>\n\n🌍 اللغة الافتراضية للترجمة: <b>%s</b>",
			langLabel(s.SelectedLanguage)),
		kb)
}

func showLanguages(chatID int64) {
	kb := [][]map[string]interface{}{
		{btn("🇸🇦 العربية", "lang_ar", "primary"), btn("🇺🇸 English", "lang_en", "primary")},
		{btn("🇹🇷 Türkçe", "lang_tr", "primary"), btn("🇫🇷 Français", "lang_fr", "primary")},
		{btn("🇪🇸 Español", "lang_es", "primary"), btn("🇩🇪 Deutsch", "lang_de", "primary")},
		{btn("🔙 رجوع", "menu_settings", "primary")},
	}
	sendMessage(chatID, "🌍 <b>اختر اللغة الافتراضية للترجمة:</b>", kb)
}

func showTranslateMenu(chatID int64, s *UserSession) {
	if s.Draft == nil || (s.Draft.Caption == "" && len(s.Draft.Media) == 0) {
		sendMessage(chatID, "⚠️ لا يوجد محتوى للترجمة.", backKeyboard())
		return
	}
	kb := [][]map[string]interface{}{
		{btn("🇸🇦 العربية", "trg_ar", "primary"), btn("🇺🇸 English", "trg_en", "primary")},
		{btn("🇹🇷 Türkçe", "trg_tr", "primary"), btn("🇫🇷 Français", "trg_fr", "primary")},
		{btn("🇪🇸 Español", "trg_es", "primary"), btn("🇩🇪 Deutsch", "trg_de", "primary")},
		{btn("❌ إلغاء", "pub_back", "danger")},
	}
	sendMessage(chatID, "🌍 <b>إلى أي لغة تريد الترجمة؟</b>", kb)
}

// ===================================================
// Inline Translate button on published posts
// ===================================================

func handleInlineTranslate(cq *CallbackQuery) {
	var text string
	msg := cq.Message
	if msg != nil {
		if cleanString(msg.Caption) != "" {
			text = msg.Caption
		} else if cleanString(msg.Text) != "" {
			text = msg.Text
		} else if msg.ReplyToMessage != nil {
			if cleanString(msg.ReplyToMessage.Caption) != "" {
				text = msg.ReplyToMessage.Caption
			} else if cleanString(msg.ReplyToMessage.Text) != "" {
				text = msg.ReplyToMessage.Text
			}
		}
	}
	if strings.TrimSpace(text) == "" {
		answerCallback(cq.ID, "لا يوجد نص للترجمة.", true)
		return
	}

	src := detectLang(text)
	dst := "ar"
	if src == "ar" {
		dst = "en"
	}

	translated := translateText(text, src, dst)
	if strings.HasPrefix(translated, "تعذرت") || strings.HasPrefix(translated, "لا يوجد") {
		answerCallback(cq.ID, translated, true)
		return
	}

	// If short → show in alert. If long → send privately to user.
	if len([]rune(translated)) <= 180 {
		answerCallback(cq.ID, translated, true)
		return
	}
	answerCallback(cq.ID, "✅ تم إرسال الترجمة في الخاص.", false)
	sendMessage(cq.From.ID,
		fmt.Sprintf("🌐 <b>ترجمة المنشور</b> (%s → %s):\n\n%s",
			langLabel(src), langLabel(dst), htmlEscape(translated)),
		nil)
}

// ===================================================
// Inline Query mode
// ===================================================

func handleInlineQuery(iq *InlineQuery) {
	query := cleanString(iq.Query)
	if query == "" {
		// Show hint result
		results := []map[string]interface{}{
			{
				"type":        "article",
				"id":          "hint_" + iq.ID,
				"title":       "✏️ اكتب نصاً بعد اسم البوت",
				"description": "مثال: @BotName مرحبا",
				"input_message_content": map[string]interface{}{
					"message_text": "اكتب نصاً بعد اسم البوت لترجمته.",
				},
			},
		}
		answerInlineQueryResults(iq.ID, results)
		return
	}

	src := detectLang(query)
	targets := []string{"ar", "en", "tr", "fr", "es", "de"}

	results := make([]map[string]interface{}, len(targets))
	var wg sync.WaitGroup
	for i, lang := range targets {
		if lang == src {
			// Show original as one option
			results[i] = map[string]interface{}{
				"type":        "article",
				"id":          fmt.Sprintf("inline_%s_%s", lang, iq.ID),
				"title":       flagEmoji(lang) + " " + langLabel(lang) + " (النص الأصلي)",
				"description": truncate(query, 100),
				"input_message_content": map[string]interface{}{
					"message_text": query,
					"parse_mode":   "HTML",
				},
			}
			continue
		}
		wg.Add(1)
		go func(i int, lang string) {
			defer wg.Done()
			translated := translateText(query, src, lang)
			if strings.HasPrefix(translated, "تعذرت") || strings.HasPrefix(translated, "لا يوجد") {
				results[i] = nil
				return
			}
			results[i] = map[string]interface{}{
				"type":        "article",
				"id":          fmt.Sprintf("inline_%s_%s", lang, iq.ID),
				"title":       flagEmoji(lang) + " " + langLabel(lang),
				"description": truncate(translated, 100),
				"input_message_content": map[string]interface{}{
					"message_text": htmlEscape(translated),
					"parse_mode":   "HTML",
				},
			}
		}(i, lang)
	}
	wg.Wait()

	cleaned := make([]map[string]interface{}, 0, len(results))
	for _, r := range results {
		if r != nil {
			cleaned = append(cleaned, r)
		}
	}
	answerInlineQueryResults(iq.ID, cleaned)
}

func answerInlineQueryResults(iqID string, results []map[string]interface{}) {
	payload := map[string]interface{}{
		"inline_query_id": iqID,
		"results":         results,
		"cache_time":      30,
		"is_personal":     true,
	}
	if _, err := callTelegramAPI("answerInlineQuery", payload); err != nil {
		log.Printf("answerInlineQuery: %v", err)
	}
}

func flagEmoji(lang string) string {
	switch lang {
	case "ar":
		return "🇸🇦"
	case "en":
		return "🇺🇸"
	case "tr":
		return "🇹🇷"
	case "fr":
		return "🇫🇷"
	case "es":
		return "🇪🇸"
	case "de":
		return "🇩🇪"
	}
	return "🌐"
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

// detectLang - rough but functional
func detectLang(text string) string {
	hasArabic := false
	hasCyrillic := false
	hasLatin := false
	hasTurkish := false
	for _, r := range text {
		switch {
		case r >= 0x0600 && r <= 0x06FF:
			hasArabic = true
		case r >= 0x0400 && r <= 0x04FF:
			hasCyrillic = true
		case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
			hasLatin = true
		case r == 'ı' || r == 'İ' || r == 'ş' || r == 'Ş' ||
			r == 'ğ' || r == 'Ğ' || r == 'ç' || r == 'Ç' ||
			r == 'ö' || r == 'Ö' || r == 'ü' || r == 'Ü':
			hasTurkish = true
		}
	}
	if hasArabic {
		return "ar"
	}
	if hasTurkish {
		return "tr"
	}
	if hasCyrillic {
		return "en" // fallback (we don't support ru)
	}
	if hasLatin {
		return "en"
	}
	return "en"
}

func translateText(text, src, dst string) string {
	clean := cleanString(text)
	if clean == "" {
		return "لا يوجد نص محدد للترجمة."
	}
	if src == dst {
		return clean
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

	q := url.Values{}
	q.Set("q", clean)
	q.Set("langpair", src+"|"+dst)
	if apiKey != "" {
		q.Set("key", apiKey)
	}
	full := baseURL + "?" + q.Encode()

	req, err := http.NewRequest(http.MethodGet, full, nil)
	if err != nil {
		log.Printf("translateText: new request: %v", err)
		return "تعذرت الترجمة حالياً."
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		log.Printf("translateText: http: %v", err)
		return "تعذرت الترجمة حالياً."
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("translateText: read: %v", err)
		return "تعذرت الترجمة حالياً."
	}
	if resp.StatusCode != http.StatusOK {
		log.Printf("translateText: status %d: %s", resp.StatusCode, truncate(string(body), 200))
		return "تعذرت الترجمة حالياً."
	}

	var result struct {
		ResponseData struct {
			TranslatedText string `json:"translatedText"`
		} `json:"responseData"`
		ResponseStatus interface{} `json:"responseStatus"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("translateText: json: %v", err)
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
		log.Printf("translateText: non-200 status: %s", truncate(string(body), 200))
		return "تعذرت الترجمة (قد يكون الحد اليومي مستنفداً)."
	}
	if result.ResponseData.TranslatedText == "" {
		return "تعذرت ترجمة النص."
	}
	return result.ResponseData.TranslatedText
}

// legacy wrapper (Arabic) — kept for compatibility
func translateTo(text, target string) string {
	src := detectLang(text)
	if src == target {
		src = "en"
	}
	return translateText(text, src, target)
}

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
	replacer := strings.NewReplacer(
		"\u200b", "",
		"\u200c", "",
		"\u200d", "",
		"\u2060", "",
		"\ufeff", "",
		"\u2800", "",
		"ㅤ",     "",
	)
	s = replacer.Replace(s)
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
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
