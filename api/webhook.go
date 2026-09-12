package handler

// ===================================================
// Telegram Channel Publishing Manager — Full Extended Build
// Features:
//  • Multi-channel publishing (all media types)
//  • Preview + Edit + Translate + Format buttons
//  • Signature / QR Code / Post stats / Hashtags
//  • Inline Mode (translate to 6 languages)
//  • Channel admin commands (/del, /pin, /unpin, /tr, /react, /info)
//  • URL shortener / Split long text / Silent + Protect / Auto-pin
//  • Announcement channel + Discussion group comments
//  • Backup / Restore via /export & /import
// ===================================================

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
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
	MessageID      int            `json:"message_id"`
	Chat           Chat           `json:"chat"`
	From           *User          `json:"from"`
	Text           string         `json:"text"`
	Caption        string         `json:"caption"`
	MediaGroupID   string         `json:"media_group_id"`
	Photo          []PhotoSize    `json:"photo"`
	Video          *Video         `json:"video"`
	Voice          *Voice         `json:"voice"`
	Audio          *Audio         `json:"audio"`
	Document       *Document      `json:"document"`
	Animation      *Animation     `json:"animation"`
	Sticker        *Sticker       `json:"sticker"`
	Location       *Location      `json:"location"`
	Contact        *Contact       `json:"contact"`
	Poll           *Poll          `json:"poll"`
	ForwardOrigin  *ForwardOrigin `json:"forward_origin"`
	ReplyToMessage *Message       `json:"reply_to_message"`
}

type PhotoSize struct {
	FileID string `json:"file_id"`
}

type Video struct {
	FileID string `json:"file_id"`
}

type Voice struct {
	FileID   string `json:"file_id"`
	Duration int    `json:"duration"`
}

type Audio struct {
	FileID    string `json:"file_id"`
	Duration  int    `json:"duration"`
	Title     string `json:"title"`
	Performer string `json:"performer"`
}

type Document struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
}

type Animation struct {
	FileID string `json:"file_id"`
}

type Sticker struct {
	FileID string `json:"file_id"`
	Emoji  string `json:"emoji"`
}

type Location struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type Contact struct {
	PhoneNumber string `json:"phone_number"`
	FirstName   string `json:"first_name"`
	LastName    string `json:"last_name"`
}

type Poll struct {
	Question string       `json:"question"`
	Options  []PollOption `json:"options"`
	Type     string       `json:"type"`
}

type PollOption struct {
	Text string `json:"text"`
}

type ForwardOrigin struct {
	Type string `json:"type"`
	Chat *Chat  `json:"chat"`
}

type Chat struct {
	ID            int64    `json:"id"`
	Title         string   `json:"title"`
	Type          string   `json:"type"`
	LinkedChatID  int64    `json:"linked_chat_id"`
	PinnedMessage *Message `json:"pinned_message"`
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
	Type          string
	FileID        string
	FileName      string
	Title         string
	Performer     string
	Duration      int
	Emoji         string
	Latitude      float64
	Longitude     float64
	PhoneNumber   string
	FirstName     string
	LastName      string
	Question      string
	Options       []string
	IsQuiz        bool
	CorrectOption int
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
	Media         []MediaItem
	MediaGroupID  string
	Caption       string
	Buttons       []InlineButton
	SuggestedTags []string
}

type PublishResult struct {
	ChannelID string
	Title     string
	Success   bool
	Err       string
	MessageID int
	Link      string
}

// ===================================================
// States
// ===================================================

const (
	StateIdle             = ""
	StateAwaitChannelID   = "await_channel_id"
	StateAwaitButtonName  = "await_button_name"
	StateAwaitButtonURL   = "await_button_url"
	StateAwaitEditCaption = "await_edit_caption"
	StateAwaitText        = "await_text"
	StateAwaitSignature   = "await_signature"
	StateAwaitQR          = "await_qr"
	StateAwaitImport      = "await_import"
)

// ===================================================
// UserSession
// ===================================================

type UserSession struct {
	UserID            int64
	Draft             *Draft
	State             string
	Channels          []Channel
	SelectedChannels  map[string]bool
	SelectedLanguage  string
	Signature         string
	AnnouncementID    string
	Silent            bool
	Protect           bool
	AutoPin           bool
	ShortenURLs       bool
	SplitText         bool
	ConfirmPublish    bool
	LastPreviewMsgID  int
	LastMediaTime     time.Time
	LastPublishTime   time.Time
	PendingButtonName string
	PendingConfirm    bool
	PendingChannels   []Channel
	Guard             map[string]time.Time
}

type SessionBackup struct {
	V                int       `json:"v"`
	Channels         []Channel `json:"channels,omitempty"`
	Signature        string    `json:"signature,omitempty"`
	SelectedLanguage string    `json:"language,omitempty"`
	AnnouncementID   string    `json:"announce,omitempty"`
	Silent           bool      `json:"silent,omitempty"`
	Protect          bool      `json:"protect,omitempty"`
	AutoPin          bool      `json:"pin,omitempty"`
	ShortenURLs      bool      `json:"short,omitempty"`
	SplitText        bool      `json:"split,omitempty"`
}

// ===================================================
// In-memory state
// ===================================================

var (
	sessionsMu sync.RWMutex
	sessions   = make(map[int64]*UserSession)
	httpClient = &http.Client{Timeout: 45 * time.Second}
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
			ConfirmPublish:   true,
			SplitText:        true,
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
// Telegram API helper
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

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("%s: marshal: %w", method, err)
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

func editMessageText(chatID int64, messageID int, text string, keyboard [][]map[string]interface{}) error {
	if messageID == 0 {
		return errors.New("edit: msgID=0")
	}
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
	_, err := callTelegramAPI("editMessageText", payload)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "message is not modified") {
			return nil
		}
		return err
	}
	return nil
}

func deleteMessage(chatID int64, messageID int) {
	if messageID == 0 {
		return
	}
	if _, err := callTelegramAPI("deleteMessage", map[string]interface{}{
		"chat_id": chatID, "message_id": messageID,
	}); err != nil {
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
// Webhook entry
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

	// ===== Channel admin commands (before private-chat logic) =====
	if m.Chat.Type == "channel" && strings.HasPrefix(text, "/") {
		if handleChannelCommand(m, text) {
			return
		}
	}

	if strings.HasPrefix(text, "/") {
		fields := strings.Fields(text)
		cmd := fields[0]
		if i := strings.Index(cmd, "@"); i != -1 {
			cmd = cmd[:i]
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
			s.PendingConfirm = false
			sendMessage(userID, "❌ تم إلغاء العملية.", nil)
			showMainMenu(userID, s)
			return
		case "/channels":
			showChannelsMenu(userID, s)
			return
		case "/publish":
			s.State = StateAwaitText
			sendMessage(userID, "📤 أرسل المحتوى (نص، صورة، فيديو، صوت، ملف، موقع، استفتاء، ألبوم…).", nil)
			return
		case "/languages":
			showLanguages(userID)
			return
		case "/settings":
			showSettings(userID, s)
			return
		case "/signature":
			s.State = StateAwaitSignature
			sendMessage(userID, "✍️ أرسل نص التوقيع.\n• أرسل <code>-</code> لحذفه.", nil)
			return
		case "/qr":
			if len(fields) < 2 {
				s.State = StateAwaitQR
				sendMessage(userID, "📱 أرسل الرابط.", nil)
				return
			}
			sendQRCode(userID, fields[1])
			return
		case "/export":
			exportSession(userID, s)
			return
		case "/import":
			s.State = StateAwaitImport
			sendMessage(userID, "📥 الصق رمز النسخة (<code>BACKUP:v1:…</code>).", nil)
			return
		}
	}

	if s.State == StateAwaitImport {
		s.State = StateIdle
		if importSession(userID, s, text) {
			sendMessage(userID, "✅ تم الاستيراد.", backKeyboard())
		} else {
			sendMessage(userID, "⚠️ الرمز غير صالح.", nil)
		}
		return
	}

	switch s.State {
	case StateAwaitChannelID:
		handleAddChannelInput(userID, s, text)
		return
	case StateAwaitEditCaption:
		if s.Draft == nil {
			s.State = StateIdle
			sendMessage(userID, "⚠️ لا يوجد منشور.", nil)
			return
		}
		s.Draft.Caption = markdownToHTML(text)
		s.State = StateIdle
		upsertPreview(userID, s)
		sendMessage(userID,
			"✅ تم تحديث النص.\n\n<i>هل تريد تنسيقاً (عريض، مائل…)؟ اضغط 🎨 تنسيق في المعاينة.</i>",
			nil)
		return
	case StateAwaitButtonName:
		s.PendingButtonName = cleanString(text)
		if s.PendingButtonName == "" {
			sendMessage(userID, "⚠️ اسم الزر فارغ.", nil)
			return
		}
		s.State = StateAwaitButtonURL
		sendMessage(userID, "🔗 أرسل الرابط (http:// أو https://).", nil)
		return
	case StateAwaitButtonURL:
		link := cleanString(text)
		if !strings.HasPrefix(link, "http://") && !strings.HasPrefix(link, "https://") {
			sendMessage(userID, "⚠️ رابط غير صالح.", nil)
			return
		}
		if s.Draft == nil {
			s.Draft = &Draft{}
		}
		s.Draft.Buttons = append(s.Draft.Buttons, InlineButton{Text: s.PendingButtonName, URL: link})
		s.PendingButtonName = ""
		s.State = StateIdle
		upsertPreview(userID, s)
		return
	case StateAwaitSignature:
		v := cleanString(text)
		s.State = StateIdle
		if v == "-" {
			s.Signature = ""
			sendMessage(userID, "🗑 تم حذف التوقيع.", backKeyboard())
			return
		}
		s.Signature = v
		sendMessage(userID, "✅ تم حفظ التوقيع.", backKeyboard())
		return
	case StateAwaitQR:
		s.State = StateIdle
		if !strings.HasPrefix(text, "http://") && !strings.HasPrefix(text, "https://") {
			sendMessage(userID, "⚠️ رابط غير صالح.", nil)
			return
		}
		sendQRCode(userID, text)
		return
	}

	if hasMedia(m) {
		handleIncomingMedia(userID, s, m)
		return
	}
	if text != "" {
		handleIncomingText(userID, s, m)
	}
}

// ===================================================
// Channel admin commands
// ===================================================

func handleChannelCommand(m *Message, text string) bool {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return false
	}
	cmd := fields[0]
	if i := strings.Index(cmd, "@"); i != -1 {
		cmd = cmd[:i]
	}

	known := map[string]bool{
		"/help": true, "/del": true, "/pin": true, "/unpin": true,
		"/tr": true, "/react": true, "/info": true,
	}
	if !known[cmd] {
		return false
	}

	chatID := m.Chat.ID

	switch cmd {
	case "/help":
		helpText := "<b>📢 أوامر القناة</b>\n\n" +
			"<b>بدون رد على رسالة:</b>\n" +
			"• /info — معلومات القناة\n" +
			"• /unpin — إلغاء تثبيت الرسالة الحالية\n" +
			"• /help — هذه القائمة\n\n" +
			"<b>بالرد على رسالة:</b>\n" +
			"• /del — حذف الرسالة\n" +
			"• /pin — تثبيت الرسالة\n" +
			"• /tr — ترجمة إلى العربية\n" +
			"• /react 👍 — إضافة تفاعل\n" +
			"<i>مثال: /react 🔥</i>"
		sendMessage(chatID, helpText, nil)
		deleteMessage(chatID, m.MessageID)
		return true
	case "/info":
		chat, err := getChat(strconv.FormatInt(chatID, 10))
		if err != nil {
			sendMessage(chatID, "❌ تعذر جلب المعلومات.", nil)
			deleteMessage(chatID, m.MessageID)
			return true
		}
		msg := fmt.Sprintf("<b>📢 معلومات القناة</b>\n\n🆔 <code>%d</code>\n📛 <b>%s</b>",
			chat.ID, htmlEscape(chat.Title))
		sendMessage(chatID, msg, nil)
		deleteMessage(chatID, m.MessageID)
		return true
	case "/unpin":
		if _, err := callTelegramAPI("unpinChatMessage", map[string]interface{}{
			"chat_id": chatID,
		}); err != nil {
			log.Printf("unpinChatMessage: %v", err)
		}
		sendMessage(chatID, "📌 تم إلغاء التثبيت.", nil)
		deleteMessage(chatID, m.MessageID)
		return true
	}

	if m.ReplyToMessage == nil {
		sendMessage(chatID, "⚠️ هذا الأمر يتطلب الرد على رسالة.", nil)
		deleteMessage(chatID, m.MessageID)
		return true
	}
	replyID := m.ReplyToMessage.MessageID

	switch cmd {
	case "/del":
		deleteMessage(chatID, replyID)
		deleteMessage(chatID, m.MessageID)
	case "/pin":
		pinChannelMessage(chatID, replyID)
		deleteMessage(chatID, m.MessageID)
	case "/tr":
		var src string
		if m.ReplyToMessage.Text != "" {
			src = m.ReplyToMessage.Text
		} else if m.ReplyToMessage.Caption != "" {
			src = m.ReplyToMessage.Caption
		}
		if strings.TrimSpace(src) == "" {
			sendMessage(chatID, "⚠️ لا يوجد نص للترجمة.", nil)
			deleteMessage(chatID, m.MessageID)
			return true
		}
		lang := detectLang(src)
		translated := translateText(src, lang, "ar")
		if strings.HasPrefix(translated, "تعذرت") || strings.HasPrefix(translated, "لا يوجد") {
			sendMessage(chatID, "⚠️ "+translated, nil)
		} else {
			_, _ = callTelegramAPI("sendMessage", map[string]interface{}{
				"chat_id":                  chatID,
				"text":                     "🌐 <b>ترجمة:</b>\n\n" + htmlEscape(translated),
				"parse_mode":               "HTML",
				"reply_to_message_id":      replyID,
				"disable_web_page_preview": true,
			})
		}
		deleteMessage(chatID, m.MessageID)
	case "/react":
		emoji := "👍"
		if len(fields) >= 2 {
			emoji = fields[1]
		}
		_, err := callTelegramAPI("setMessageReaction", map[string]interface{}{
			"chat_id":    chatID,
			"message_id": replyID,
			"reaction": []map[string]interface{}{
				{"type": "emoji", "emoji": emoji},
			},
		})
		if err != nil {
			log.Printf("setMessageReaction: %v", err)
			sendMessage(chatID, "⚠️ تعذر إضافة التفاعل.", nil)
		}
		deleteMessage(chatID, m.MessageID)
	}
	return true
}

// ===================================================
// Media detection
// ===================================================

func hasMedia(m *Message) bool {
	return len(m.Photo) > 0 || m.Video != nil || m.Voice != nil || m.Audio != nil ||
		m.Document != nil || m.Animation != nil || m.Sticker != nil ||
		m.Location != nil || m.Contact != nil || m.Poll != nil
}

func extractMediaItem(m *Message) *MediaItem {
	switch {
	case len(m.Photo) > 0:
		return &MediaItem{Type: "photo", FileID: m.Photo[len(m.Photo)-1].FileID}
	case m.Video != nil:
		return &MediaItem{Type: "video", FileID: m.Video.FileID}
	case m.Voice != nil:
		return &MediaItem{Type: "voice", FileID: m.Voice.FileID, Duration: m.Voice.Duration}
	case m.Audio != nil:
		return &MediaItem{Type: "audio", FileID: m.Audio.FileID, Duration: m.Audio.Duration, Title: m.Audio.Title, Performer: m.Audio.Performer}
	case m.Document != nil:
		return &MediaItem{Type: "document", FileID: m.Document.FileID, FileName: m.Document.FileName}
	case m.Animation != nil:
		return &MediaItem{Type: "animation", FileID: m.Animation.FileID}
	case m.Sticker != nil:
		return &MediaItem{Type: "sticker", FileID: m.Sticker.FileID, Emoji: m.Sticker.Emoji}
	case m.Location != nil:
		return &MediaItem{Type: "location", Latitude: m.Location.Latitude, Longitude: m.Location.Longitude}
	case m.Contact != nil:
		return &MediaItem{Type: "contact", PhoneNumber: m.Contact.PhoneNumber, FirstName: m.Contact.FirstName, LastName: m.Contact.LastName}
	case m.Poll != nil:
		opts := make([]string, len(m.Poll.Options))
		for i, o := range m.Poll.Options {
			opts[i] = o.Text
		}
		return &MediaItem{
			Type:     "poll",
			Question: m.Poll.Question,
			Options:  opts,
			IsQuiz:   m.Poll.Type == "quiz",
		}
	}
	return nil
}

func handleIncomingMedia(userID int64, s *UserSession, m *Message) {
	item := extractMediaItem(m)
	if item == nil {
		return
	}

	groupID := m.MediaGroupID
	now := time.Now()

	newDraft := s.Draft == nil
	if !newDraft {
		if groupID == "" && !s.LastMediaTime.IsZero() && now.Sub(s.LastMediaTime) > 3*time.Second {
			newDraft = true
		} else if groupID != "" && s.Draft.MediaGroupID != "" && s.Draft.MediaGroupID != groupID {
			newDraft = true
		}
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
		s.Draft.Caption = markdownToHTML(m.Caption)
	}
	s.LastMediaTime = now
	upsertPreview(userID, s)
}

func handleIncomingText(userID int64, s *UserSession, m *Message) {
	clean := markdownToHTML(m.Text)
	if clean == "" {
		return
	}
	if s.Draft != nil && len(s.Draft.Media) > 0 {
		s.Draft.Caption = clean
		upsertPreview(userID, s)
		return
	}
	if s.Draft == nil {
		s.Draft = &Draft{}
	}
	s.Draft.Caption = clean
	upsertPreview(userID, s)
}

// ===================================================
// Preview
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

func upsertPreview(userID int64, s *UserSession) {
	if s.Draft == nil {
		return
	}
	desc := describeDraft(s.Draft)
	kb := previewKeyboard(s)
	if s.LastPreviewMsgID != 0 {
		if err := editMessageText(userID, s.LastPreviewMsgID, desc, kb); err == nil {
			return
		}
		s.LastPreviewMsgID = 0
	}
	s.LastPreviewMsgID = sendMessage(userID, desc, kb)
}

func previewKeyboard(s *UserSession) [][]map[string]interface{} {
	return [][]map[string]interface{}{
		{
			btn("🚀 نشر", "preview_publish", "success"),
			btn("📢 قنوات", "preview_select_channels", "primary"),
		},
		{
			btn("✏️ تعديل النص", "preview_edit_caption", "primary"),
			btn("🎨 تنسيق", "preview_format", "primary"),
		},
		{
			btn("🌍 ترجمة", "preview_translate", "primary"),
			btn("🏷️ هاشتاقات", "preview_hashtags", "primary"),
		},
		{
			btn("🔘 أزرار", "preview_buttons", "primary"),
			btn("🗑 حذف الأزرار", "preview_clear_buttons", "danger"),
		},
		{
			btn("⚙️ خيارات النشر", "preview_options", "primary"),
		},
		{
			btn("❌ إلغاء", "preview_cancel", "danger"),
		},
	}
}

func optionsKeyboard(s *UserSession) [][]map[string]interface{} {
	silent := "🔔 إشعار"
	if s.Silent {
		silent = "🔕 صامت"
	}
	pin := "📍 بدون تثبيت"
	if s.AutoPin {
		pin = "📌 تثبيت تلقائي"
	}
	protect := "🔓 غير محمي"
	if s.Protect {
		protect = "🔒 محمي"
	}
	shorten := "🔗 بدون اختصار"
	if s.ShortenURLs {
		shorten = "🔗 اختصار تلقائي"
	}
	split := "📄 بدون تقسيم"
	if s.SplitText {
		split = "✂️ تقسيم تلقائي"
	}
	return [][]map[string]interface{}{
		{btn(silent, "opt_silent", "primary"), btn(pin, "opt_pin", "primary")},
		{btn(protect, "opt_protect", "primary"), btn(shorten, "opt_shorten", "primary")},
		{btn(split, "opt_split", "primary")},
		{btn("✅ تأكيد قبل النشر: "+onOff(s.ConfirmPublish), "opt_confirm", "primary")},
		{btn("🏠 رجوع للمعاينة", "preview_back", "primary")},
	}
}

func onOff(b bool) string {
	if b {
		return "مفعّل"
	}
	return "معطّل"
}

func describeDraft(d *Draft) string {
	var b strings.Builder
	b.WriteString("👀 <b>معاينة المنشور</b>\n\n")

	if len(d.Media) > 1 {
		fmt.Fprintf(&b, "📚 <b>النوع:</b> ألبوم (%d)\n", len(d.Media))
	} else if len(d.Media) == 1 {
		fmt.Fprintf(&b, "📎 <b>النوع:</b> %s\n", mediaTypeLabel(d.Media[0]))
	} else {
		b.WriteString("📝 <b>النوع:</b> نص\n")
	}

	if d.Caption != "" {
		b.WriteString("\n📝 <b>النص:</b>\n")
		b.WriteString(truncate(d.Caption, 600))
		b.WriteString("\n")
		stats := analyzeCaption(d.Caption)
		fmt.Fprintf(&b, "\n📊 <i>%d حرف • %d كلمة • ~%d ث قراءة</i>\n",
			stats.Chars, stats.Words, stats.ReadSec)
		fmt.Fprintf(&b, "🔗 روابط: %d • @منشن: %d • #هاشتاق: %d • 😊: %d%%\n",
			stats.Links, stats.Mentions, stats.Hashtags, stats.EmojiPct)
		fmt.Fprintf(&b, "📈 <i>الصعوبة: %s</i>\n", stats.Difficulty)
	} else {
		b.WriteString("\n<i>(لا يوجد نص)</i>\n")
	}

	if len(d.Buttons) > 0 {
		b.WriteString("\n🔘 <b>أزرار:</b>\n")
		for _, x := range d.Buttons {
			fmt.Fprintf(&b, "• %s → %s\n", htmlEscape(x.Text), htmlEscape(x.URL))
		}
	}
	return b.String()
}

func mediaTypeLabel(m MediaItem) string {
	switch m.Type {
	case "photo":
		return "📷 صورة"
	case "video":
		return "🎥 فيديو"
	case "voice":
		return "🎤 رسالة صوتية"
	case "audio":
		return "🎵 ملف صوتي"
	case "document":
		return "📎 ملف"
	case "animation":
		return "🎞 صورة متحركة"
	case "sticker":
		return "🎨 ملصق"
	case "location":
		return "📍 موقع"
	case "contact":
		return "👤 جهة اتصال"
	case "poll":
		return "📊 استفتاء"
	}
	return m.Type
}

// ===================================================
// Caption analysis
// ===================================================

type CaptionStats struct {
	Chars      int
	Words      int
	Links      int
	Mentions   int
	Hashtags   int
	EmojiPct   int
	ReadSec    int
	Difficulty string
}

var (
	reURL     = regexp.MustCompile(`https?://[^\s<>"]+`)
	reMention = regexp.MustCompile(`@[A-Za-z0-9_]{3,}`)
	reHashtag = regexp.MustCompile(`#[\p{L}\p{N}_]+`)
)

func analyzeCaption(text string) CaptionStats {
	plain := stripHTMLTags(text)
	runes := []rune(plain)
	stats := CaptionStats{Chars: len(runes), Words: len(strings.Fields(plain))}
	stats.Links = len(reURL.FindAllString(plain, -1))
	stats.Mentions = len(reMention.FindAllString(plain, -1))
	stats.Hashtags = len(reHashtag.FindAllString(plain, -1))

	emojiCount := 0
	for _, r := range plain {
		if isEmoji(r) {
			emojiCount++
		}
	}
	if len(runes) > 0 {
		stats.EmojiPct = (emojiCount * 100) / len(runes)
	}
	stats.ReadSec = stats.Words / 3
	if stats.ReadSec < 1 {
		stats.ReadSec = 1
	}
	avgWord := 0
	if stats.Words > 0 {
		avgWord = len(runes) / stats.Words
	}
	switch {
	case avgWord <= 5:
		stats.Difficulty = "سهل"
	case avgWord <= 7:
		stats.Difficulty = "متوسط"
	default:
		stats.Difficulty = "صعب"
	}
	return stats
}

func stripHTMLTags(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		if r == '<' {
			inTag = true
			continue
		}
		if r == '>' {
			inTag = false
			continue
		}
		if !inTag {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isEmoji(r rune) bool {
	return (r >= 0x1F300 && r <= 0x1FAFF) ||
		(r >= 0x2600 && r <= 0x27BF) ||
		(r >= 0x1F000 && r <= 0x1F02F) ||
		r == 0x2764
}

// ===================================================
// Hashtag generator
// ===================================================

var stopWords = map[string]bool{
	"في": true, "على": true, "من": true, "إلى": true, "عن": true, "مع": true,
	"هذا": true, "هذه": true, "ذلك": true, "التي": true, "الذي": true, "هو": true, "هي": true,
	"the": true, "a": true, "an": true, "and": true, "or": true, "of": true,
	"to": true, "in": true, "is": true, "are": true, "was": true, "were": true,
	"for": true, "on": true, "at": true, "by": true, "with": true, "from": true,
	"this": true, "that": true, "it": true, "be": true, "have": true, "has": true,
	"will": true, "would": true, "can": true, "not": true,
}

func suggestHashtags(caption string) []string {
	plain := stripHTMLTags(caption)
	plain = reURL.ReplaceAllString(plain, " ")
	plain = reMention.ReplaceAllString(plain, " ")
	plain = reHashtag.ReplaceAllString(plain, " ")

	words := strings.FieldsFunc(plain, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	freq := make(map[string]int)
	for _, w := range words {
		wl := strings.ToLower(w)
		if len([]rune(wl)) < 4 {
			continue
		}
		if stopWords[wl] {
			continue
		}
		freq[wl]++
	}
	type wc struct {
		w string
		c int
	}
	list := make([]wc, 0, len(freq))
	for w, c := range freq {
		list = append(list, wc{w, c})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].c != list[j].c {
			return list[i].c > list[j].c
		}
		return list[i].w < list[j].w
	})
	if len(list) > 8 {
		list = list[:8]
	}
	out := make([]string, 0, len(list))
	for _, x := range list {
		out = append(out, "#"+x.w)
	}
	return out
}

// ===================================================
// Markdown → HTML
// ===================================================

var (
	reBold      = regexp.MustCompile(`\*\*([^*\n]+?)\*\*`)
	reUnderline = regexp.MustCompile(`__([^_\n]+?)__`)
	reStrike    = regexp.MustCompile(`~~([^~\n]+?)~~`)
	reSpoiler   = regexp.MustCompile(`\|\|([^\|\n]+?)\|\|`)
	reCode      = regexp.MustCompile("`([^`\n]+?)`")
	reItalic    = regexp.MustCompile(`\*([^*\n]+?)\*`)
	reItalicU   = regexp.MustCompile(`_([^_\n]+?)_`)
)

func markdownToHTML(s string) string {
	s = htmlEscape(s)
	s = reBold.ReplaceAllString(s, "<b>$1</b>")
	s = reUnderline.ReplaceAllString(s, "<u>$1</u>")
	s = reStrike.ReplaceAllString(s, "<s>$1</s>")
	s = reSpoiler.ReplaceAllString(s, "<tg-spoiler>$1</tg-spoiler>")
	s = reCode.ReplaceAllString(s, "<code>$1</code>")
	s = reItalic.ReplaceAllString(s, "<i>$1</i>")
	s = reItalicU.ReplaceAllString(s, "<i>$1</i>")
	return s
}

// ===================================================
// Format toggles
// ===================================================

func toggleWrap(caption, tag string) string {
	openTag := "<" + tag + ">"
	closeTag := "</" + tag + ">"
	t := strings.TrimSpace(caption)
	if strings.HasPrefix(t, openTag) && strings.HasSuffix(t, closeTag) {
		return strings.TrimSuffix(strings.TrimPrefix(t, openTag), closeTag)
	}
	return openTag + t + closeTag
}

func stripAllFormat(caption string) string {
	var b strings.Builder
	inTag := false
	for _, r := range caption {
		if r == '<' {
			inTag = true
			continue
		}
		if r == '>' {
			inTag = false
			continue
		}
		if !inTag {
			b.WriteRune(r)
		}
	}
	out := b.String()
	for strings.Contains(out, "\n\n\n") {
		out = strings.ReplaceAll(out, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(out)
}

func applyFormat(s *UserSession, action string) {
	if s.Draft == nil || s.Draft.Caption == "" {
		return
	}
	switch action {
	case "fmt_bold":
		s.Draft.Caption = toggleWrap(s.Draft.Caption, "b")
	case "fmt_italic":
		s.Draft.Caption = toggleWrap(s.Draft.Caption, "i")
	case "fmt_underline":
		s.Draft.Caption = toggleWrap(s.Draft.Caption, "u")
	case "fmt_strike":
		s.Draft.Caption = toggleWrap(s.Draft.Caption, "s")
	case "fmt_code":
		s.Draft.Caption = toggleWrap(s.Draft.Caption, "code")
	case "fmt_spoiler":
		s.Draft.Caption = toggleWrap(s.Draft.Caption, "tg-spoiler")
	case "fmt_quote":
		s.Draft.Caption = toggleWrap(s.Draft.Caption, "blockquote")
	case "fmt_clean":
		s.Draft.Caption = cleanString(s.Draft.Caption)
	case "fmt_upper":
		s.Draft.Caption = strings.ToUpper(stripAllFormat(s.Draft.Caption))
	case "fmt_lower":
		s.Draft.Caption = strings.ToLower(stripAllFormat(s.Draft.Caption))
	case "fmt_strip":
		s.Draft.Caption = stripAllFormat(s.Draft.Caption)
	}
}

func formatKeyboard() [][]map[string]interface{} {
	return [][]map[string]interface{}{
		{
			btn("🅱️ عريض", "fmt_bold", "primary"),
			btn("𝘐 مائل", "fmt_italic", "primary"),
		},
		{
			btn("U̲ تحته خط", "fmt_underline", "primary"),
			btn("S̶ يتوسطه", "fmt_strike", "primary"),
		},
		{
			btn("</> كود", "fmt_code", "primary"),
			btn("▒ إخفاء", "fmt_spoiler", "primary"),
		},
		{
			btn("❝ اقتباس", "fmt_quote", "primary"),
			btn("🧹 تنظيف", "fmt_clean", "primary"),
		},
		{
			btn("🔠 UPPER", "fmt_upper", "primary"),
			btn("🔡 lower", "fmt_lower", "primary"),
		},
		{
			btn("♻️ إزالة كل التنسيق", "fmt_strip", "danger"),
		},
		{
			btn("🔙 رجوع للمعاينة", "fmt_back", "primary"),
		},
	}
}

// ===================================================
// URL shortener
// ===================================================

func shortenURL(longURL string) (string, error) {
	api := "https://is.gd/create.php?format=simple&url=" + url.QueryEscape(longURL)
	req, err := http.NewRequest(http.MethodGet, api, nil)
	if err != nil {
		return "", err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("shortener status %d", resp.StatusCode)
	}
	result := strings.TrimSpace(string(body))
	if !strings.HasPrefix(result, "http") {
		return "", errors.New("shortener failed")
	}
	return result, nil
}

func shortenAllURLs(text string) string {
	return reURL.ReplaceAllStringFunc(text, func(u string) string {
		short, err := shortenURL(u)
		if err != nil {
			return u
		}
		return short
	})
}

// ===================================================
// Split long text
// ===================================================

func splitText(text string, maxLen int) []string {
	runes := []rune(text)
	if len(runes) <= maxLen {
		return []string{text}
	}
	var parts []string
	for len(runes) > maxLen {
		cut := maxLen
		for i := maxLen - 1; i > maxLen-300 && i > 0; i-- {
			if runes[i] == '\n' {
				cut = i + 1
				break
			}
		}
		parts = append(parts, strings.TrimRight(string(runes[:cut]), "\n"))
		runes = runes[cut:]
	}
	if len(runes) > 0 {
		parts = append(parts, string(runes))
	}
	return parts
}

// ===================================================
// Publishing
// ===================================================

func publishToChannels(s *UserSession, channels []Channel, d *Draft) []PublishResult {
	caption := d.Caption
	if s.Signature != "" {
		if caption != "" {
			caption += "\n\n" + s.Signature
		} else {
			caption = s.Signature
		}
	}
	if s.ShortenURLs && caption != "" {
		caption = shortenAllURLs(caption)
	}

	results := make([]PublishResult, 0, len(channels))
	for _, ch := range channels {
		res := PublishResult{ChannelID: ch.ID, Title: ch.Title, Success: true}
		msgIDs, err := publishOne(ch.ID, d, caption, s)
		if err != nil {
			res.Success = false
			res.Err = err.Error()
			log.Printf("publish to %s failed: %v", ch.ID, err)
		} else if len(msgIDs) > 0 {
			res.MessageID = msgIDs[0]
			res.Link = buildPostLink(ch.ID, msgIDs[0])
			if s.AutoPin {
				pinChannelMessage(ch.ID, msgIDs[0])
			}
		}
		results = append(results, res)
	}
	return results
}

func publishOne(channelID string, d *Draft, caption string, s *UserSession) ([]int, error) {
	opts := map[string]interface{}{
		"disable_notification": s.Silent,
		"protect_content":      s.Protect,
	}

	if len(d.Media) == 0 {
		if s.SplitText && len([]rune(caption)) > 4000 {
			return publishSplitText(channelID, caption, d.Buttons, opts)
		}
		id, err := publishText(channelID, caption, d.Buttons, opts)
		if err != nil {
			return nil, err
		}
		return []int{id}, nil
	}

	if len(d.Media) == 1 {
		item := d.Media[0]
		id, err := publishSingleMedia(channelID, item, caption, d.Buttons, opts)
		if err != nil {
			return nil, err
		}
		return []int{id}, nil
	}

	allGroupable := true
	for _, it := range d.Media {
		if it.Type != "photo" && it.Type != "video" {
			allGroupable = false
			break
		}
	}
	if allGroupable {
		id, err := publishMediaGroup(channelID, d.Media, caption, d.Buttons, opts)
		if err != nil {
			return nil, err
		}
		return []int{id}, nil
	}

	var ids []int
	for i, item := range d.Media {
		cap := ""
		if i == 0 {
			cap = caption
		}
		id, err := publishSingleMedia(channelID, item, cap, nil, opts)
		if err != nil {
			return ids, err
		}
		ids = append(ids, id)
	}
	sendButtonsMarker(channelID, d.Buttons, opts, ids)
	return ids, nil
}

func publishSplitText(channelID, text string, buttons []InlineButton, opts map[string]interface{}) ([]int, error) {
	parts := splitText(text, 4000)
	var ids []int
	for i, part := range parts {
		header := fmt.Sprintf("<i>(%d/%d)</i>\n\n", i+1, len(parts))
		var kb []InlineButton
		if i == len(parts)-1 {
			kb = buttons
		}
		payload := map[string]interface{}{
			"chat_id":                  channelID,
			"text":                     header + part,
			"parse_mode":               "HTML",
			"disable_web_page_preview": true,
			"reply_markup":             map[string]interface{}{"inline_keyboard": userButtonsToInline(kb)},
		}
		for k, v := range opts {
			payload[k] = v
		}
		raw, err := callTelegramAPI("sendMessage", payload)
		if err != nil {
			return ids, err
		}
		var r struct {
			MessageID int `json:"message_id"`
		}
		_ = json.Unmarshal(raw, &r)
		ids = append(ids, r.MessageID)
	}
	return ids, nil
}

func publishSingleMedia(channelID string, item MediaItem, caption string, buttons []InlineButton, opts map[string]interface{}) (int, error) {
	var method string
	payload := map[string]interface{}{
		"chat_id":      channelID,
		"reply_markup": map[string]interface{}{"inline_keyboard": userButtonsToInline(buttons)},
	}
	switch item.Type {
	case "photo":
		method = "sendPhoto"
		payload["photo"] = item.FileID
	case "video":
		method = "sendVideo"
		payload["video"] = item.FileID
	case "voice":
		method = "sendVoice"
		payload["voice"] = item.FileID
	case "audio":
		method = "sendAudio"
		payload["audio"] = item.FileID
		if item.Title != "" {
			payload["title"] = item.Title
		}
		if item.Performer != "" {
			payload["performer"] = item.Performer
		}
	case "document":
		method = "sendDocument"
		payload["document"] = item.FileID
	case "animation":
		method = "sendAnimation"
		payload["animation"] = item.FileID
	case "sticker":
		method = "sendSticker"
		payload["sticker"] = item.FileID
		delete(payload, "reply_markup")
	case "location":
		method = "sendLocation"
		payload["latitude"] = item.Latitude
		payload["longitude"] = item.Longitude
	case "contact":
		method = "sendContact"
		payload["phone_number"] = item.PhoneNumber
		payload["first_name"] = item.FirstName
		if item.LastName != "" {
			payload["last_name"] = item.LastName
		}
	case "poll":
		method = "sendPoll"
		payload["question"] = item.Question
		optBytes, _ := json.Marshal(item.Options)
		payload["options"] = json.RawMessage(optBytes)
		if item.IsQuiz {
			payload["type"] = "quiz"
			payload["correct_option_id"] = item.CorrectOption
		}
		delete(payload, "reply_markup")
	default:
		return 0, fmt.Errorf("unsupported media type: %s", item.Type)
	}

	if caption != "" {
		switch item.Type {
		case "photo", "video", "audio", "document", "animation", "voice":
			payload["caption"] = caption
			payload["parse_mode"] = "HTML"
		}
	}

	for k, v := range opts {
		payload[k] = v
	}

	raw, err := callTelegramAPI(method, payload)
	if err != nil {
		return 0, err
	}
	var res struct {
		MessageID int `json:"message_id"`
	}
	_ = json.Unmarshal(raw, &res)
	return res.MessageID, nil
}

func publishText(channelID, text string, extra []InlineButton, opts map[string]interface{}) (int, error) {
	if strings.TrimSpace(text) == "" {
		return 0, errors.New("نص فارغ")
	}
	payload := map[string]interface{}{
		"chat_id":                  channelID,
		"text":                     text,
		"parse_mode":               "HTML",
		"reply_markup":             map[string]interface{}{"inline_keyboard": userButtonsToInline(extra)},
		"disable_web_page_preview": true,
	}
	for k, v := range opts {
		payload[k] = v
	}
	raw, err := callTelegramAPI("sendMessage", payload)
	if err != nil {
		return 0, err
	}
	var res struct {
		MessageID int `json:"message_id"`
	}
	_ = json.Unmarshal(raw, &res)
	return res.MessageID, nil
}

func publishMediaGroup(channelID string, items []MediaItem, caption string, extra []InlineButton, opts map[string]interface{}) (int, error) {
	media := make([]InputMedia, 0, len(items))
	for i, it := range items {
		m := InputMedia{Type: it.Type, Media: it.FileID}
		if i == 0 && caption != "" {
			m.Caption = caption
		}
		media = append(media, m)
	}
	payload := map[string]interface{}{
		"chat_id": channelID,
		"media":   media,
	}
	for k, v := range opts {
		payload[k] = v
	}
	raw, err := callTelegramAPI("sendMediaGroup", payload)
	if err != nil {
		return 0, err
	}
	var msgs []struct {
		MessageID int `json:"message_id"`
	}
	_ = json.Unmarshal(raw, &msgs)

	firstMsgID := 0
	if len(msgs) > 0 {
		firstMsgID = msgs[0].MessageID
	}
	sendButtonsMarker(channelID, extra, opts, []int{firstMsgID})
	return firstMsgID, nil
}

func sendButtonsMarker(channelID string, extra []InlineButton, opts map[string]interface{}, replyIDs []int) {
	btnPayload := map[string]interface{}{
		"chat_id":      channelID,
		"text":         "▼",
		"reply_markup": map[string]interface{}{"inline_keyboard": userButtonsToInline(extra)},
	}
	if len(replyIDs) > 0 && replyIDs[0] != 0 {
		btnPayload["reply_to_message_id"] = replyIDs[0]
	}
	for k, v := range opts {
		if k != "protect_content" {
			btnPayload[k] = v
		}
	}
	if _, err := callTelegramAPI("sendMessage", btnPayload); err != nil {
		log.Printf("buttons marker: %v", err)
		delete(btnPayload, "reply_to_message_id")
		if _, err2 := callTelegramAPI("sendMessage", btnPayload); err2 != nil {
			log.Printf("buttons marker 2: %v", err2)
		}
	}
}

func pinChannelMessage(channelID int64, msgID int) {
	if msgID == 0 {
		return
	}
	_, err := callTelegramAPI("pinChatMessage", map[string]interface{}{
		"chat_id":              channelID,
		"message_id":           msgID,
		"disable_notification": true,
	})
	if err != nil {
		log.Printf("pinChatMessage: %v", err)
	}
}

func buildPostLink(channelID string, msgID int) string {
	if msgID == 0 {
		return ""
	}
	if strings.HasPrefix(channelID, "@") {
		return fmt.Sprintf("https://t.me/%s/%d", strings.TrimPrefix(channelID, "@"), msgID)
	}
	if strings.HasPrefix(channelID, "-100") {
		return fmt.Sprintf("https://t.me/c/%s/%d", strings.TrimPrefix(channelID, "-100"), msgID)
	}
	return ""
}

func userButtonsToInline(b []InlineButton) [][]map[string]interface{} {
	out := make([][]map[string]interface{}, 0, len(b)+1)
	for _, x := range b {
		out = append(out, []map[string]interface{}{
			{"text": x.Text, "url": x.URL},
		})
	}
	out = append(out, []map[string]interface{}{btn("🌐 Translate", "translate", "primary")})
	return out
}

// ===================================================
// Announcement + Discussion group comments
// ===================================================

func sendAnnouncement(s *UserSession, results []PublishResult) {
	if s.AnnouncementID != "" {
		var lines []string
		for _, r := range results {
			if r.Success && r.Link != "" {
				lines = append(lines, fmt.Sprintf("📢 <a href=\"%s\">%s</a>", r.Link, htmlEscape(r.Title)))
			}
		}
		if len(lines) > 0 {
			payload := map[string]interface{}{
				"chat_id":                  s.AnnouncementID,
				"text":                     "📣 <b>منشور جديد</b>\n\n" + strings.Join(lines, "\n"),
				"parse_mode":               "HTML",
				"disable_web_page_preview": true,
			}
			if _, err := callTelegramAPI("sendMessage", payload); err != nil {
				log.Printf("announcement: %v", err)
			}
		}
	}

	for _, r := range results {
		if !r.Success || r.Link == "" {
			continue
		}
		chat, err := getChat(r.ChannelID)
		if err != nil || chat.LinkedChatID == 0 {
			continue
		}
		payload := map[string]interface{}{
			"chat_id":                  chat.LinkedChatID,
			"text":                     fmt.Sprintf("🔗 <a href=\"%s\">رابط المنشور</a>", r.Link),
			"parse_mode":               "HTML",
			"disable_web_page_preview": true,
		}
		if _, err := callTelegramAPI("sendMessage", payload); err != nil {
			log.Printf("discussion comment: %v", err)
		}
	}
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

	if data == "translate" {
		handleInlineTranslate(cq)
		return
	}

	switch {
	// ---- menus ----
	case data == "menu":
		deleteMessage(chatID, cq.Message.MessageID)
		showMainMenu(chatID, s)
	case data == "menu_channels":
		deleteMessage(chatID, cq.Message.MessageID)
		showChannelsMenu(chatID, s)
	case data == "menu_publish":
		deleteMessage(chatID, cq.Message.MessageID)
		s.State = StateAwaitText
		sendMessage(chatID, "📤 أرسل المحتوى (نص، صورة، فيديو، صوت، ملف، موقع، استفتاء، ألبوم…).", nil)
	case data == "menu_help":
		deleteMessage(chatID, cq.Message.MessageID)
		showHelp(chatID)
	case data == "menu_settings":
		deleteMessage(chatID, cq.Message.MessageID)
		showSettings(chatID, s)
	case data == "menu_languages":
		deleteMessage(chatID, cq.Message.MessageID)
		showLanguages(chatID)
	case data == "menu_qr":
		deleteMessage(chatID, cq.Message.MessageID)
		s.State = StateAwaitQR
		sendMessage(chatID, "📱 أرسل الرابط.", backKeyboard())
	case data == "menu_signature":
		deleteMessage(chatID, cq.Message.MessageID)
		s.State = StateAwaitSignature
		sendMessage(chatID, "✍️ أرسل نص التوقيع، أو <code>-</code> لحذفه.", backKeyboard())
	case data == "menu_announce":
		deleteMessage(chatID, cq.Message.MessageID)
		showAnnouncePicker(chatID, s)
	case data == "menu_export":
		deleteMessage(chatID, cq.Message.MessageID)
		exportSession(chatID, s)
	case data == "menu_import":
		deleteMessage(chatID, cq.Message.MessageID)
		s.State = StateAwaitImport
		sendMessage(chatID, "📥 الصق رمز النسخة (<code>BACKUP:v1:…</code>).", backKeyboard())

	// ---- channels ----
	case data == "ch_add":
		deleteMessage(chatID, cq.Message.MessageID)
		s.State = StateAwaitChannelID
		sendMessage(chatID,
			"➕ أرسل معرف القناة:\n• <code>@username</code>\n• <code>-100xxxxxxxxxx</code>",
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

	// ---- preview ----
	case data == "preview_publish":
		deleteMessage(chatID, cq.Message.MessageID)
		s.LastPreviewMsgID = 0
		showChannelSelector(chatID, s)
	case data == "preview_select_channels":
		deleteMessage(chatID, cq.Message.MessageID)
		s.LastPreviewMsgID = 0
		showChannelSelector(chatID, s)
	case data == "preview_edit_caption":
		deleteMessage(chatID, cq.Message.MessageID)
		s.LastPreviewMsgID = 0
		s.State = StateAwaitEditCaption
		sendMessage(chatID,
			"✏️ <b>أرسل النص الجديد</b>\n\n"+
				"• اكتب النص بأي شكل (عادي، عربي، إنجليزي…)\n"+
				"• بعد الإرسال، اضغط زر <b>🎨 تنسيق</b> لتطبيق <b>عريض</b> أو <i>مائل</i> أو غيرها بضغطة واحدة.",
			nil)
	case data == "preview_translate":
		deleteMessage(chatID, cq.Message.MessageID)
		s.LastPreviewMsgID = 0
		showTranslateMenu(chatID, s)
	case data == "preview_buttons":
		deleteMessage(chatID, cq.Message.MessageID)
		s.LastPreviewMsgID = 0
		s.State = StateAwaitButtonName
		sendMessage(chatID, "🔘 أرسل اسم الزر.", nil)
	case data == "preview_clear_buttons":
		if s.Draft != nil {
			s.Draft.Buttons = nil
		}
		deleteMessage(chatID, cq.Message.MessageID)
		s.LastPreviewMsgID = 0
		upsertPreview(chatID, s)
	case data == "preview_cancel":
		clearDraft(s)
		s.State = StateIdle
		s.PendingConfirm = false
		deleteMessage(chatID, cq.Message.MessageID)
		sendMessage(chatID, "❌ تم الإلغاء.", nil)
		showMainMenu(chatID, s)
	case data == "preview_back":
		deleteMessage(chatID, cq.Message.MessageID)
		s.LastPreviewMsgID = 0
		upsertPreview(chatID, s)
	case data == "preview_options":
		deleteMessage(chatID, cq.Message.MessageID)
		sendMessage(chatID, "⚙️ <b>خيارات النشر</b>", optionsKeyboard(s))

	// ---- format submenu ----
	case data == "preview_format":
		deleteMessage(chatID, cq.Message.MessageID)
		if s.Draft == nil || s.Draft.Caption == "" {
			answerCallback(cq.ID, "أرسل نصاً أولاً.", true)
			return
		}
		sendMessage(chatID,
			"🎨 <b>تنسيق النص</b>\n\n"+
				"اختر التنسيق — يمكنك دمج أكثر من واحد بضغطات متتالية.\n"+
				"<i>اضغط نفس الزر مرة أخرى لإلغاء التنسيق.</i>",
			formatKeyboard())
	case data == "fmt_back":
		deleteMessage(chatID, cq.Message.MessageID)
		s.LastPreviewMsgID = 0
		upsertPreview(chatID, s)
	case data == "fmt_bold" || data == "fmt_italic" || data == "fmt_underline" ||
		data == "fmt_strike" || data == "fmt_code" || data == "fmt_spoiler" ||
		data == "fmt_quote" || data == "fmt_clean" || data == "fmt_upper" ||
		data == "fmt_lower" || data == "fmt_strip":
		applyFormat(s, data)
		_ = editMessageText(chatID, cq.Message.MessageID,
			"🎨 <b>تنسيق النص</b>\n\n<i>آخر تحديث مُطبّق ✓</i>",
			formatKeyboard())

	// ---- options toggles ----
	case data == "opt_silent":
		s.Silent = !s.Silent
		refreshOptionsMessage(chatID, s, cq.Message.MessageID)
	case data == "opt_pin":
		s.AutoPin = !s.AutoPin
		refreshOptionsMessage(chatID, s, cq.Message.MessageID)
	case data == "opt_protect":
		s.Protect = !s.Protect
		refreshOptionsMessage(chatID, s, cq.Message.MessageID)
	case data == "opt_shorten":
		s.ShortenURLs = !s.ShortenURLs
		refreshOptionsMessage(chatID, s, cq.Message.MessageID)
	case data == "opt_split":
		s.SplitText = !s.SplitText
		refreshOptionsMessage(chatID, s, cq.Message.MessageID)
	case data == "opt_confirm":
		s.ConfirmPublish = !s.ConfirmPublish
		refreshOptionsMessage(chatID, s, cq.Message.MessageID)

	// ---- hashtags ----
	case data == "preview_hashtags":
		deleteMessage(chatID, cq.Message.MessageID)
		showHashtagSuggestions(chatID, s)
	case data == "ht_add":
		if s.Draft != nil && len(s.Draft.SuggestedTags) > 0 {
			tags := strings.Join(s.Draft.SuggestedTags, " ")
			if s.Draft.Caption != "" {
				s.Draft.Caption += "\n\n" + tags
			} else {
				s.Draft.Caption = tags
			}
			s.Draft.SuggestedTags = nil
		}
		deleteMessage(chatID, cq.Message.MessageID)
		upsertPreview(chatID, s)
	case data == "ht_regen":
		deleteMessage(chatID, cq.Message.MessageID)
		showHashtagSuggestions(chatID, s)
	case data == "ht_cancel":
		if s.Draft != nil {
			s.Draft.SuggestedTags = nil
		}
		deleteMessage(chatID, cq.Message.MessageID)
		upsertPreview(chatID, s)

	// ---- channel selector ----
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
		handlePubConfirm(chatID, s, cq)
	case data == "pub_final":
		handlePubFinal(chatID, s, cq)
	case data == "pub_abort":
		s.PendingConfirm = false
		deleteMessage(chatID, cq.Message.MessageID)
		showChannelSelector(chatID, s)
	case data == "pub_back":
		deleteMessage(chatID, cq.Message.MessageID)
		s.LastPreviewMsgID = 0
		upsertPreview(chatID, s)

	// ---- languages ----
	case strings.HasPrefix(data, "lang_"):
		lang := strings.TrimPrefix(data, "lang_")
		s.SelectedLanguage = lang
		deleteMessage(chatID, cq.Message.MessageID)
		sendMessage(chatID, fmt.Sprintf("✅ اللغة: <b>%s</b>", langLabel(lang)), backKeyboard())
	case strings.HasPrefix(data, "trg_"):
		lang := strings.TrimPrefix(data, "trg_")
		if s.Draft == nil || s.Draft.Caption == "" {
			answerCallback(cq.ID, "لا يوجد نص.", true)
			return
		}
		deleteMessage(chatID, cq.Message.MessageID)
		s.LastPreviewMsgID = 0
		src := detectLang(s.Draft.Caption)
		translated := translateText(s.Draft.Caption, src, lang)
		if strings.HasPrefix(translated, "تعذرت") || strings.HasPrefix(translated, "لا يوجد") {
			sendMessage(chatID, "⚠️ "+translated, nil)
		} else {
			s.Draft.Caption = translated
		}
		upsertPreview(chatID, s)

	// ---- announcement ----
	case strings.HasPrefix(data, "ann_ch_"):
		idx, _ := strconv.Atoi(strings.TrimPrefix(data, "ann_ch_"))
		if idx < 0 || idx >= len(s.Channels) {
			return
		}
		s.AnnouncementID = s.Channels[idx].ID
		deleteMessage(chatID, cq.Message.MessageID)
		sendMessage(chatID, "✅ قناة الإعلانات: <b>"+htmlEscape(s.Channels[idx].Title)+"</b>", backKeyboard())
	case data == "ann_off":
		s.AnnouncementID = ""
		deleteMessage(chatID, cq.Message.MessageID)
		sendMessage(chatID, "🗑 تم تعطيل قناة الإعلانات.", backKeyboard())
	}

	answerCallback(cq.ID, "", false)
}

func refreshOptionsMessage(chatID int64, s *UserSession, msgID int) {
	_ = editMessageText(chatID, msgID, "⚙️ <b>خيارات النشر</b>", optionsKeyboard(s))
}

// ===================================================
// Hashtag suggestions UI
// ===================================================

func showHashtagSuggestions(chatID int64, s *UserSession) {
	if s.Draft == nil || s.Draft.Caption == "" {
		sendMessage(chatID, "⚠️ لا يوجد نص لتوليد هاشتاقات.", nil)
		return
	}
	tags := suggestHashtags(s.Draft.Caption)
	if len(tags) == 0 {
		sendMessage(chatID, "⚠️ لم أستخرج كلمات مفتاحية.", nil)
		return
	}
	s.Draft.SuggestedTags = tags
	kb := [][]map[string]interface{}{
		{btn("➕ إضافة الهاشتاقات", "ht_add", "success")},
		{btn("🔄 توليد مرة أخرى", "ht_regen", "primary"), btn("❌ إلغاء", "ht_cancel", "danger")},
	}
	sendMessage(chatID,
		fmt.Sprintf("🏷️ <b>اقتراحات هاشتاقات:</b>\n\n<code>%s</code>",
			htmlEscape(strings.Join(tags, " "))),
		kb)
}

// ===================================================
// Channel management
// ===================================================

func handleAddChannelInput(userID int64, s *UserSession, text string) {
	input := cleanString(text)
	if !strings.HasPrefix(input, "@") && !strings.HasPrefix(input, "-100") {
		sendMessage(userID, "⚠️ صيغة غير صحيحة.", nil)
		return
	}
	for _, c := range s.Channels {
		if strings.EqualFold(c.ID, input) {
			sendMessage(userID, "⚠️ مضافة مسبقاً.", nil)
			s.State = StateIdle
			showChannelsMenu(userID, s)
			return
		}
	}
	chat, err := getChat(input)
	if err != nil {
		log.Printf("getChat(%s): %v", input, err)
		sendMessage(userID, "⚠️ تعذر الوصول للقناة. تأكد من رفع البوت مشرفاً.", nil)
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
	sendMessage(userID, fmt.Sprintf("✅ أُضيفت: <b>%s</b>\n🆔 <code>%s</code>",
		htmlEscape(title), htmlEscape(input)), nil)
	showChannelsMenu(userID, s)
}

func handleDeleteChannel(userID int64, s *UserSession, idx int) {
	if idx < 0 || idx >= len(s.Channels) {
		return
	}
	removed := s.Channels[idx]
	s.Channels = append(s.Channels[:idx], s.Channels[idx+1:]...)
	delete(s.SelectedChannels, removed.ID)
	if s.AnnouncementID == removed.ID {
		s.AnnouncementID = ""
	}
	sendMessage(userID, "🗑 حُذفت: <b>"+htmlEscape(removed.Title)+"</b>", nil)
	showDeleteChannelMenu(userID, s)
}

func handleRefreshChannel(userID int64, s *UserSession, idx int) {
	if idx < 0 || idx >= len(s.Channels) {
		return
	}
	chat, err := getChat(s.Channels[idx].ID)
	if err != nil {
		sendMessage(userID, "❌ "+err.Error(), nil)
		return
	}
	if chat.Title != "" {
		s.Channels[idx].Title = chat.Title
	}
	sendMessage(userID, "🔄 تم التحديث.", nil)
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
	sendMessage(userID, fmt.Sprintf("🔄 حُدّثت %d/%d.", updated, len(s.Channels)), nil)
	showChannelsMenu(userID, s)
}

// ===================================================
// Channel selector
// ===================================================

func showChannelSelector(chatID int64, s *UserSession) {
	if len(s.Channels) == 0 {
		sendMessage(chatID, "⚠️ لا توجد قنوات مضافة.", [][]map[string]interface{}{
			{btn("📢 إدارة القنوات", "menu_channels", "primary")},
		})
		return
	}
	if s.Draft == nil {
		sendMessage(chatID, "⚠️ انتهت الجلسة، أعد إرسال المحتوى.", nil)
		showMainMenu(chatID, s)
		return
	}
	text, kb := buildChannelSelector(s)
	sendMessage(chatID, text, kb)
}

func refreshChannelSelectorMessage(chatID int64, s *UserSession, msgID int) {
	text, kb := buildChannelSelector(s)
	_ = editMessageText(chatID, msgID, text, kb)
}

func buildChannelSelector(s *UserSession) (string, [][]map[string]interface{}) {
	var b strings.Builder
	b.WriteString("📢 <b>اختر القنوات</b>\n\n")
	var kb [][]map[string]interface{}
	var row []map[string]interface{}
	for i, ch := range s.Channels {
		mark := "☐"
		if s.SelectedChannels[ch.ID] {
			mark = "☑"
		}
		row = append(row, btn(fmt.Sprintf("%s %s", mark, truncate(ch.Title, 20)),
			fmt.Sprintf("sel_%d", i), "primary"))
		if len(row) == 2 {
			kb = append(kb, row)
			row = nil
		}
	}
	if len(row) > 0 {
		kb = append(kb, row)
	}
	kb = append(kb,
		[]map[string]interface{}{btn("✅ الكل", "pub_all", "primary"), btn("❌ إلغاء التحديد", "pub_none", "danger")},
		[]map[string]interface{}{btn("🚀 نشر", "pub_confirm", "success")},
		[]map[string]interface{}{btn("🔙 رجوع", "pub_back", "primary")},
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

func handlePubConfirm(chatID int64, s *UserSession, cq *CallbackQuery) {
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
	s.PendingChannels = targets

	if !s.ConfirmPublish {
		doPublish(chatID, s, cq.Message.MessageID)
		return
	}

	var b strings.Builder
	b.WriteString("⚠️ <b>تأكيد النشر</b>\n\nسيُنشر المنشور في:\n\n")
	for _, ch := range targets {
		fmt.Fprintf(&b, "📢 <b>%s</b>\n", htmlEscape(ch.Title))
	}
	kb := [][]map[string]interface{}{
		{btn("✅ نعم، انشر", "pub_final", "success")},
		{btn("❌ إلغاء", "pub_abort", "danger")},
	}
	_ = editMessageText(chatID, cq.Message.MessageID, b.String(), kb)
	s.PendingConfirm = true
}

func handlePubFinal(chatID int64, s *UserSession, cq *CallbackQuery) {
	if !s.PendingConfirm {
		answerCallback(cq.ID, "لا يوجد تأكيد معلّق.", true)
		return
	}
	s.PendingConfirm = false
	doPublish(chatID, s, cq.Message.MessageID)
}

func doPublish(chatID int64, s *UserSession, msgID int) {
	if !s.LastPublishTime.IsZero() && time.Since(s.LastPublishTime) < 5*time.Second {
		sendMessage(chatID, "⏳ انتظر قليلاً قبل النشر مجدداً.", nil)
		return
	}
	if s.Draft == nil || len(s.PendingChannels) == 0 {
		sendMessage(chatID, "⚠️ لا يوجد محتوى أو قنوات.", nil)
		return
	}
	s.LastPublishTime = time.Now()
	targets := s.PendingChannels
	s.PendingChannels = nil

	deleteMessage(chatID, msgID)

	draft := s.Draft
	results := publishToChannels(s, targets, draft)
	clearDraft(s)

	sendAnnouncement(s, results)

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
	fmt.Fprintf(&b, "✅ نجح: %d\n❌ فشل: %d\n\n", ok, fail)
	for _, r := range results {
		if r.Success {
			if r.Link != "" {
				fmt.Fprintf(&b, "✅ <b>%s</b>\n🔗 %s\n", htmlEscape(r.Title), r.Link)
			} else {
				fmt.Fprintf(&b, "✅ <b>%s</b>\n", htmlEscape(r.Title))
			}
		} else {
			fmt.Fprintf(&b, "❌ <b>%s</b>\n<code>%s</code>\n", htmlEscape(r.Title), htmlEscape(r.Err))
		}
	}
	return b.String()
}

// ===================================================
// Menus
// ===================================================

func backKeyboard() [][]map[string]interface{} {
	return [][]map[string]interface{}{{btn("🔙 رجوع", "menu", "primary")}}
}

func showMainMenu(chatID int64, s *UserSession) {
	kb := [][]map[string]interface{}{
		{
			btn("📢 إدارة القنوات", "menu_channels", "primary"),
			btn("📤 نشر", "menu_publish", "success"),
		},
		{
			btn("🌍 الترجمة", "menu_languages", "primary"),
			btn("📱 QR Code", "menu_qr", "primary"),
		},
		{
			btn("📣 قناة الإعلانات", "menu_announce", "primary"),
			btn("⚙️ الإعدادات", "menu_settings", "primary"),
		},
		{
			btn("📤 تصدير الإعدادات", "menu_export", "primary"),
			btn("📥 استيراد الإعدادات", "menu_import", "primary"),
		},
		{
			btn("❓ المساعدة", "menu_help", "primary"),
		},
	}
	sendMessage(chatID, "<b>🎛️ لوحة التحكم</b>\n\nاختر العملية:", kb)
}

func showHelp(chatID int64) {
	helpText := "<b>❓ المساعدة</b>\n\n" +
		"<b>الأوامر (في الخاص):</b>\n" +
		"• /start /menu — لوحة التحكم\n" +
		"• /publish — بدء النشر\n" +
		"• /channels — إدارة القنوات\n" +
		"• /languages — لغات الترجمة\n" +
		"• /signature — التوقيع التلقائي\n" +
		"• /qr &lt;url&gt; — توليد QR\n" +
		"• /export /import — نسخ احتياطي\n" +
		"• /cancel — إلغاء\n\n" +
		"<b>أوامر القناة (يجب أن يكون البوت مشرفاً):</b>\n" +
		"• /info — معلومات القناة\n" +
		"• /help — أوامر القناة\n" +
		"• /unpin — إلغاء تثبيت الرسالة الحالية\n" +
		"• /del (بالرد) — حذف الرسالة\n" +
		"• /pin (بالرد) — تثبيت الرسالة\n" +
		"• /tr (بالرد) — ترجمة الرسالة\n" +
		"• /react 👍 (بالرد) — إضافة تفاعل\n\n" +
		"<b>أنواع المحتوى:</b>\n" +
		"نص • صورة • فيديو • صوت • ملف • ملصق • موقع • جهة اتصال • استفتاء • ألبوم\n\n" +
		"<b>التنسيق:</b> استخدم زر 🎨 تنسيق في المعاينة للأزرار السريعة.\n\n" +
		"<b>Bulk Forward:</b> أعد توجيه أي منشور من قناة أخرى وسيُستقبل مباشرة.\n\n" +
		"<b>Inline Mode:</b> اكتب <code>@YourBotName نص</code> في أي محادثة."
	sendMessage(chatID, helpText, backKeyboard())
}

func showChannelsMenu(chatID int64, s *UserSession) {
	kb := [][]map[string]interface{}{
		{btn("➕ إضافة", "ch_add", "success"), btn("📋 قنواتي", "ch_list", "primary")},
		{btn("🗑 حذف", "ch_delete_menu", "danger"), btn("🔄 تحديث", "ch_refresh", "primary")},
		{btn("🏠 الرئيسية", "menu", "primary")},
	}
	sendMessage(chatID, "📢 <b>إدارة القنوات</b>", kb)
}

func showChannelList(chatID int64, s *UserSession) {
	if len(s.Channels) == 0 {
		sendMessage(chatID, "لا توجد قنوات.", backKeyboard())
		return
	}
	var b strings.Builder
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
		sendMessage(chatID, "لا توجد قنوات.", backKeyboard())
		return
	}
	var kb [][]map[string]interface{}
	for i, ch := range s.Channels {
		kb = append(kb, []map[string]interface{}{
			btn("🗑 "+truncate(ch.Title, 30), fmt.Sprintf("ch_del_%d", i), "danger"),
		})
	}
	kb = append(kb, []map[string]interface{}{btn("🔙 رجوع", "menu_channels", "primary")})
	sendMessage(chatID, "اختر القناة:", kb)
}

func showSettings(chatID int64, s *UserSession) {
	sig := s.Signature
	if sig == "" {
		sig = "<i>(لا يوجد)</i>"
	}
	ann := "<i>(لا توجد)</i>"
	if s.AnnouncementID != "" {
		ann = "<code>" + htmlEscape(s.AnnouncementID) + "</code>"
	}
	kb := [][]map[string]interface{}{
		{btn("🌍 اللغة", "menu_languages", "primary"), btn("✍️ التوقيع", "menu_signature", "primary")},
		{btn("📣 قناة الإعلانات", "menu_announce", "primary")},
		{btn("🏠 الرئيسية", "menu", "primary")},
	}
	sendMessage(chatID,
		fmt.Sprintf("<b>⚙️ الإعدادات</b>\n\n🌍 اللغة: <b>%s</b>\n✍️ التوقيع:\n%s\n\n📣 الإعلانات: %s",
			langLabel(s.SelectedLanguage), htmlEscape(sig), ann),
		kb)
}

func showLanguages(chatID int64) {
	kb := [][]map[string]interface{}{
		{btn("🇸🇦", "lang_ar", "primary"), btn("🇺🇸", "lang_en", "primary"), btn("🇹🇷", "lang_tr", "primary")},
		{btn("🇫🇷", "lang_fr", "primary"), btn("🇪🇸", "lang_es", "primary"), btn("🇩🇪", "lang_de", "primary")},
		{btn("🔙 رجوع", "menu_settings", "primary")},
	}
	sendMessage(chatID, "🌍 <b>اللغة الافتراضية:</b>", kb)
}

func showTranslateMenu(chatID int64, s *UserSession) {
	if s.Draft == nil || s.Draft.Caption == "" {
		sendMessage(chatID, "⚠️ لا يوجد نص.", backKeyboard())
		return
	}
	kb := [][]map[string]interface{}{
		{btn("🇸🇦", "trg_ar", "primary"), btn("🇺🇸", "trg_en", "primary"), btn("🇹🇷", "trg_tr", "primary")},
		{btn("🇫🇷", "trg_fr", "primary"), btn("🇪🇸", "trg_es", "primary"), btn("🇩🇪", "trg_de", "primary")},
		{btn("❌ إلغاء", "pub_back", "danger")},
	}
	sendMessage(chatID, "🌍 <b>ترجم إلى:</b>", kb)
}

func showAnnouncePicker(chatID int64, s *UserSession) {
	if len(s.Channels) == 0 {
		sendMessage(chatID, "أضف قنوات أولاً.", backKeyboard())
		return
	}
	var kb [][]map[string]interface{}
	for i, ch := range s.Channels {
		kb = append(kb, []map[string]interface{}{
			btn("📣 "+truncate(ch.Title, 30), fmt.Sprintf("ann_ch_%d", i), "primary"),
		})
	}
	kb = append(kb,
		[]map[string]interface{}{btn("🗑 تعطيل", "ann_off", "danger")},
		[]map[string]interface{}{btn("🔙 رجوع", "menu", "primary")},
	)
	sendMessage(chatID,
		"📣 <b>قناة الإعلانات</b>\n\nسيُرسَل رابط كل منشور جديد إلى هذه القناة.",
		kb)
}

// ===================================================
// QR
// ===================================================

func sendQRCode(chatID int64, data string) {
	qrURL := fmt.Sprintf("https://api.qrserver.com/v1/create-qr-code/?size=500x500&data=%s",
		url.QueryEscape(data))
	payload := map[string]interface{}{
		"chat_id":    chatID,
		"photo":      qrURL,
		"caption":    fmt.Sprintf("📱 <b>QR</b>\n<code>%s</code>", htmlEscape(data)),
		"parse_mode": "HTML",
	}
	if _, err := callTelegramAPI("sendPhoto", payload); err != nil {
		sendMessage(chatID, "❌ فشل: "+err.Error(), nil)
	}
}

// ===================================================
// Export / Import
// ===================================================

func exportSession(chatID int64, s *UserSession) {
	bk := SessionBackup{
		V: 1, Channels: s.Channels, Signature: s.Signature,
		SelectedLanguage: s.SelectedLanguage, AnnouncementID: s.AnnouncementID,
		Silent: s.Silent, Protect: s.Protect, AutoPin: s.AutoPin,
		ShortenURLs: s.ShortenURLs, SplitText: s.SplitText,
	}
	raw, _ := json.Marshal(bk)
	encoded := base64.StdEncoding.EncodeToString(raw)
	token := "BACKUP:v1:" + encoded
	if len(token) > 3800 {
		sendMessage(chatID, "⚠️ الإعدادات كبيرة جداً.", nil)
		return
	}
	sendMessage(chatID,
		"📤 <b>نسخة احتياطية</b>\n\nاحتفظ بهذا الرمز:\n\n<code>"+token+"</code>",
		backKeyboard())
}

func importSession(chatID int64, s *UserSession, raw string) bool {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "BACKUP:v1:") {
		return false
	}
	encoded := strings.TrimPrefix(raw, "BACKUP:v1:")
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return false
	}
	var bk SessionBackup
	if err := json.Unmarshal(data, &bk); err != nil {
		return false
	}
	if bk.Channels != nil {
		s.Channels = bk.Channels
	}
	if bk.Signature != "" {
		s.Signature = bk.Signature
	}
	if bk.SelectedLanguage != "" {
		s.SelectedLanguage = bk.SelectedLanguage
	}
	s.AnnouncementID = bk.AnnouncementID
	s.Silent = bk.Silent
	s.Protect = bk.Protect
	s.AutoPin = bk.AutoPin
	s.ShortenURLs = bk.ShortenURLs
	s.SplitText = bk.SplitText
	return true
}

// ===================================================
// Inline Translate button
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
	if strings.TrimSpace(text) == "" || strings.TrimSpace(text) == "▼" {
		if msg != nil && msg.ReplyToMessage != nil {
			if cleanString(msg.ReplyToMessage.Caption) != "" {
				text = msg.ReplyToMessage.Caption
			} else if cleanString(msg.ReplyToMessage.Text) != "" {
				text = msg.ReplyToMessage.Text
			}
		}
	}
	if strings.TrimSpace(text) == "" {
		answerCallback(cq.ID, "لا يوجد نص.", true)
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
	if len([]rune(translated)) <= 180 {
		answerCallback(cq.ID, translated, true)
		return
	}
	answerCallback(cq.ID, "✅ الترجمة في الخاص.", false)
	sendMessage(cq.From.ID,
		fmt.Sprintf("🌐 <b>%s → %s</b>\n\n%s", langLabel(src), langLabel(dst), htmlEscape(translated)),
		nil)
}

// ===================================================
// Inline Mode
// ===================================================

func handleInlineQuery(iq *InlineQuery) {
	query := cleanString(iq.Query)
	if query == "" {
		answerInlineQueryResults(iq.ID, []map[string]interface{}{
			{
				"type": "article", "id": "hint_" + iq.ID,
				"title":       "✏️ اكتب نصاً بعد اسم البوت",
				"description": "مثال: @BotName مرحبا",
				"input_message_content": map[string]interface{}{
					"message_text": "اكتب نصاً بعد اسم البوت لترجمته.",
				},
			},
		})
		return
	}
	src := detectLang(query)
	targets := []string{"ar", "en", "tr", "fr", "es", "de"}
	results := make([]map[string]interface{}, len(targets))
	var wg sync.WaitGroup
	for i, lang := range targets {
		if lang == src {
			results[i] = map[string]interface{}{
				"type": "article", "id": fmt.Sprintf("inline_%s_%s", lang, iq.ID),
				"title":       flagEmoji(lang) + " " + langLabel(lang) + " (الأصلي)",
				"description": truncate(query, 100),
				"input_message_content": map[string]interface{}{
					"message_text": htmlEscape(query), "parse_mode": "HTML",
				},
			}
			continue
		}
		wg.Add(1)
		go func(i int, lang string) {
			defer wg.Done()
			tr := translateText(query, src, lang)
			if strings.HasPrefix(tr, "تعذرت") || strings.HasPrefix(tr, "لا يوجد") {
				results[i] = nil
				return
			}
			results[i] = map[string]interface{}{
				"type": "article", "id": fmt.Sprintf("inline_%s_%s", lang, iq.ID),
				"title":       flagEmoji(lang) + " " + langLabel(lang),
				"description": truncate(tr, 100),
				"input_message_content": map[string]interface{}{
					"message_text": htmlEscape(tr), "parse_mode": "HTML",
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
	if _, err := callTelegramAPI("answerInlineQuery", map[string]interface{}{
		"inline_query_id": iqID, "results": results,
		"cache_time": 30, "is_personal": true,
	}); err != nil {
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

func detectLang(text string) string {
	hasArabic, hasTurkish, hasLatin := false, false, false
	for _, r := range text {
		switch {
		case r >= 0x0600 && r <= 0x06FF:
			hasArabic = true
		case r == 'ı' || r == 'İ' || r == 'ş' || r == 'Ş' ||
			r == 'ğ' || r == 'Ğ' || r == 'ç' || r == 'Ç':
			hasTurkish = true
			hasLatin = true
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			hasLatin = true
		}
	}
	if hasArabic {
		return "ar"
	}
	if hasTurkish {
		return "tr"
	}
	if hasLatin {
		return "en"
	}
	return "en"
}

func translateText(text, src, dst string) string {
	clean := cleanString(text)
	if clean == "" {
		return "لا يوجد نص."
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
	req, err := http.NewRequest(http.MethodGet, baseURL+"?"+q.Encode(), nil)
	if err != nil {
		return "تعذرت الترجمة."
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "تعذرت الترجمة."
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "تعذرت الترجمة."
	}
	var result struct {
		ResponseData struct {
			TranslatedText string `json:"translatedText"`
		} `json:"responseData"`
		ResponseStatus interface{} `json:"responseStatus"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "تعذرت الترجمة."
	}
	ok := false
	switch v := result.ResponseStatus.(type) {
	case float64:
		ok = v == 200
	case string:
		ok = v == "200"
	}
	if !ok {
		return "تعذرت الترجمة."
	}
	if result.ResponseData.TranslatedText == "" {
		return "تعذرت الترجمة."
	}
	return result.ResponseData.TranslatedText
}

// ===================================================
// Utilities
// ===================================================

func btn(text, data, style string) map[string]interface{} {
	b := map[string]interface{}{"text": text, "callback_data": data}
	if style != "" {
		b["style"] = style
	}
	return b
}

func cleanString(s string) string {
	s = strings.TrimSpace(s)
	s = strings.NewReplacer(
		"\u200b", "", "\u200c", "", "\u200d", "",
		"\u2060", "", "\ufeff", "", "\u2800", "", "ㅤ", "",
	).Replace(s)
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return strings.TrimSpace(s)
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
