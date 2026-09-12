package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Update struct {
	UpdateID      int            `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

type User struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
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

type TelegramResponse struct {
	OK     bool `json:"ok"`
	Result Chat `json:"result"`
}

type MediaItem struct {
	Type   string `json:"type"`
	FileID string `json:"file_id"`
}

// InputMedia هو الشكل المرسل فعلياً لتيليجرام ضمن sendMediaGroup.
// أضفنا HasSpoiler لدعم خاصية تغبيش المحتوى لكل عنصر ضمن الألبوم.
type InputMedia struct {
	Type       string `json:"type"`
	Media      string `json:"media"`
	Caption    string `json:"caption,omitempty"`
	HasSpoiler bool   `json:"has_spoiler,omitempty"`
}

type Channel struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// Draft يمثل مسودة النشر الحالية للمستخدم، ويحتفظ بكل الإعدادات المتقدمة
// في الذاكرة فقط (بدون أي قاعدة بيانات) طوال مدة تحضير المنشور.
type Draft struct {
	Media            []MediaItem
	MediaGroupID     string
	Caption          string
	LastBotMessageID int

	// PendingInput يحدد أي خطوة إدخال نصي ننتظرها حالياً من المستخدم
	// (مثلاً: "signature", "url_text", "url_url", "lang", "autodelete").
	// قيمة فارغة تعني أنه لا يوجد إدخال منتظر.
	PendingInput string

	Signature          string
	ProtectContent     bool
	HasSpoiler         bool
	EnableReactions    bool
	URLButtonText      string
	URLButtonURL       string
	TargetLang         string
	AutoDeleteDuration time.Duration
}

// PublishedPost يحتفظ بمعلومات كافية عن رسالة تم نشرها فعلاً في قناة،
// لتمكين ميزات لاحقة (الترجمة بلغة محددة، وتحديث عدادات التفاعل)
// دون الحاجة لأي تخزين خارجي.
type PublishedPost struct {
	TargetLang      string
	HasTranslate    bool
	URLButtonText   string
	URLButtonURL    string
	EnableReactions bool
	Reactions       map[string]int
}

var (
	userChannels = make(map[int64][]Channel)
	userDrafts   = make(map[int64]*Draft)

	// publishedPosts مفتاحه "chatID_messageID" (chatID هنا هو معرف القناة الرقمي
	// كما يعيده تيليجرام)، ويُستخدم لاسترجاع إعدادات الرسالة المنشورة لاحقاً
	// عند الضغط على أزرار الترجمة أو التفاعل.
	publishedPosts = make(map[string]*PublishedPost)

	// عميل HTTP مشترك بمهلة زمنية محددة، يُستخدم في كل الطلبات الخارجية
	httpClient = &http.Client{Timeout: 8 * time.Second}

	reactionEmojis = []string{"👍", "👎", "❤️", "🔥"}
)

func Handler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var update Update
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	token := os.Getenv("TELEGRAM_BOT_TOKEN")

	if update.CallbackQuery != nil {
		handleCallbackQuery(token, update.CallbackQuery)
		w.WriteHeader(http.StatusOK)
		return
	}

	if update.Message != nil {
		chatID := update.Message.Chat.ID
		text := update.Message.Text

		if text == "/start" {
			userName := "المستخدم"
			if update.Message.From != nil && update.Message.From.FirstName != "" {
				userName = update.Message.From.FirstName
			}

			welcomeMsg := fmt.Sprintf("مرحباً بك يا %s في بوت النشر والترجمة\n\n"+
				"1️⃣ قم برفع البوت كمشرف (Admin) في قناتك مع صلاحيات نشر الرسائل.\n"+
				"2️⃣ أرسل معرف قناتك العامة مع ( الـ @) أو ايدي القناة الخاصة (-100xxxx) لإضافتها في البوت.\n"+
				"3️⃣ يمكنك إرسال عدة قنوات وسيظهر لك البوت قائمة بأسمائها عند كل عملية نشر!\n"+
				"4️⃣ يمكنك إرسال نصوص، صور، أو فيديوهات للنشر بشكل مباشر.", userName)

			sendTelegramMessage(token, chatID, welcomeMsg, nil)
			w.WriteHeader(http.StatusOK)
			return
		}

		if strings.HasPrefix(text, "@") || strings.HasPrefix(text, "-100") {
			chTitle, err := fetchChannelTitle(token, text)
			if err != nil {
				sendTelegramMessage(token, chatID, "⚠️ تعذر الوصول للقناة! تأكد من رفع البوت مشرفاً فيها وأعد المحاولة.", nil)
			} else {
				userChannels[chatID] = append(userChannels[chatID], Channel{ID: text, Title: chTitle})
				sendTelegramMessage(token, chatID, fmt.Sprintf("✅ تم إضافة القناة بنجاح:\n📌 *%s*", chTitle), nil)
			}
			w.WriteHeader(http.StatusOK)
			return
		}

		var currentMedia *MediaItem
		if len(update.Message.Photo) > 0 {
			photoID := update.Message.Photo[len(update.Message.Photo)-1].FileID
			currentMedia = &MediaItem{Type: "photo", FileID: photoID}
		} else if update.Message.Video != nil {
			currentMedia = &MediaItem{Type: "video", FileID: update.Message.Video.FileID}
		}

		if currentMedia != nil {
			mediaGroupID := update.Message.MediaGroupID
			draft, exists := userDrafts[chatID]

			if !exists || draft.MediaGroupID != mediaGroupID || mediaGroupID == "" {
				if exists && draft.LastBotMessageID != 0 {
					deleteTelegramMessage(token, chatID, draft.LastBotMessageID)
				}

				draft = &Draft{
					Media:        []MediaItem{*currentMedia},
					MediaGroupID: mediaGroupID,
					TargetLang:   "ar",
				}
				userDrafts[chatID] = draft

				buttons := [][]map[string]interface{}{
					{
						{"text": "⏩ تخطي (بدون نص)", "callback_data": "action_skip_caption", "style": "primary"},
						{"text": "❌ إلغاء", "callback_data": "action_cancel", "style": "danger"},
					},
				}
				msgID := sendTelegramMessage(token, chatID, "📸/🎥 تم استلام المحتوى!\nهل تريد إضافة نص (كابشن) أسفل المحتوى؟\n\n- أرسل النص الآن كرسالة عادية.\n- أو اضغط على (تخطي) للنشر بدون نص.", buttons)
				draft.LastBotMessageID = msgID
			} else {
				draft.Media = append(draft.Media, *currentMedia)
			}

			w.WriteHeader(http.StatusOK)
			return
		}

		if text != "" {
			draft, hasDraft := userDrafts[chatID]

			if hasDraft && draft.LastBotMessageID != 0 {
				deleteTelegramMessage(token, chatID, draft.LastBotMessageID)
				draft.LastBotMessageID = 0
			}

			// 1) إذا كنا ننتظر إدخالاً نصياً محدداً (توقيع، زر رابط، لغة، مدة حذف تلقائي)
			if hasDraft && draft.PendingInput != "" {
				handlePendingInput(token, chatID, draft, text)
				w.WriteHeader(http.StatusOK)
				return
			}

			// 2) إذا كانت هناك مسودة تحتوي وسائط بانتظار نص الكابشن الأول
			if hasDraft && len(draft.Media) > 0 && draft.Caption == "" {
				draft.Caption = text
				askSettingsMenu(token, chatID, draft)
				w.WriteHeader(http.StatusOK)
				return
			}

			// 3) لا توجد مسودة بعد -> نص عادي جديد للنشر
			if !hasDraft {
				userDrafts[chatID] = &Draft{Caption: text, TargetLang: "ar"}
				askSettingsMenu(token, chatID, userDrafts[chatID])
				w.WriteHeader(http.StatusOK)
				return
			}

			// 4) حالة نادرة: مسودة موجودة وكل شيء معبأ مسبقاً، نعيد عرض قائمة الإعدادات فقط
			askSettingsMenu(token, chatID, draft)
		}
	}

	w.WriteHeader(http.StatusOK)
}

// handlePendingInput يعالج رسالة نصية عادية عندما تكون المسودة بانتظار
// إدخال محدد (توقيع / نص زر رابط / رابط الزر / رمز اللغة / مدة الحذف التلقائي).
func handlePendingInput(token string, chatID int64, draft *Draft, text string) {
	switch draft.PendingInput {

	case "signature":
		draft.Signature = strings.TrimSpace(text)
		draft.PendingInput = ""
		askSettingsMenu(token, chatID, draft)

	case "url_text":
		draft.URLButtonText = strings.TrimSpace(text)
		draft.PendingInput = "url_url"
		msgID := sendTelegramMessage(token, chatID, "🔗 أرسل الآن رابط الزر (يجب أن يبدأ بـ http:// أو https://):", cancelPendingKeyboard())
		draft.LastBotMessageID = msgID

	case "url_url":
		trimmed := strings.TrimSpace(text)
		if !strings.HasPrefix(trimmed, "http://") && !strings.HasPrefix(trimmed, "https://") {
			msgID := sendTelegramMessage(token, chatID, "⚠️ الرابط غير صالح، يجب أن يبدأ بـ http:// أو https://. حاول مرة أخرى:", cancelPendingKeyboard())
			draft.LastBotMessageID = msgID
			return
		}
		draft.URLButtonURL = trimmed
		draft.PendingInput = ""
		askSettingsMenu(token, chatID, draft)

	case "lang":
		lang := strings.ToLower(strings.TrimSpace(text))
		if len(lang) < 2 || len(lang) > 5 {
			msgID := sendTelegramMessage(token, chatID, "⚠️ رمز لغة غير صالح. مثال: en, fr, tr, ar. حاول مرة أخرى:", cancelPendingKeyboard())
			draft.LastBotMessageID = msgID
			return
		}
		draft.TargetLang = lang
		draft.PendingInput = ""
		askSettingsMenu(token, chatID, draft)

	case "autodelete":
		minutes, err := strconv.Atoi(strings.TrimSpace(text))
		if err != nil || minutes < 0 {
			msgID := sendTelegramMessage(token, chatID, "⚠️ أرسل رقماً صحيحاً يمثل عدد الدقائق (0 لإلغاء الحذف التلقائي):", cancelPendingKeyboard())
			draft.LastBotMessageID = msgID
			return
		}
		draft.AutoDeleteDuration = time.Duration(minutes) * time.Minute
		draft.PendingInput = ""
		askSettingsMenu(token, chatID, draft)

	default:
		draft.PendingInput = ""
		askSettingsMenu(token, chatID, draft)
	}
}

func cancelPendingKeyboard() [][]map[string]interface{} {
	return [][]map[string]interface{}{
		{{"text": "❌ رجوع للإعدادات", "callback_data": "cancel_pending", "style": "danger"}},
	}
}

func handleCallbackQuery(token string, cq *CallbackQuery) {
	chatID := cq.From.ID
	data := cq.Data

	if data == "translate" {
		var textToTranslate string

		if cleanString(cq.Message.Text) != "" {
			textToTranslate = cq.Message.Text
		} else if cleanString(cq.Message.Caption) != "" {
			textToTranslate = cq.Message.Caption
		} else if cq.Message.ReplyToMessage != nil {
			if cleanString(cq.Message.ReplyToMessage.Caption) != "" {
				textToTranslate = cq.Message.ReplyToMessage.Caption
			} else if cleanString(cq.Message.ReplyToMessage.Text) != "" {
				textToTranslate = cq.Message.ReplyToMessage.Text
			}
		}

		targetLang := "ar"
		key := fmt.Sprintf("%d_%d", cq.Message.Chat.ID, cq.Message.MessageID)
		if post, ok := publishedPosts[key]; ok && post.TargetLang != "" {
			targetLang = post.TargetLang
		}

		translatedText := translateText(textToTranslate, targetLang)
		answerCallback(token, cq.ID, translatedText, true)
		return
	}

	if strings.HasPrefix(data, "react_") {
		emoji := strings.TrimPrefix(data, "react_")
		key := fmt.Sprintf("%d_%d", cq.Message.Chat.ID, cq.Message.MessageID)
		post, ok := publishedPosts[key]
		if !ok {
			answerCallback(token, cq.ID, "", false)
			return
		}
		if post.Reactions == nil {
			post.Reactions = make(map[string]int)
		}
		post.Reactions[emoji]++
		newKeyboard := buildInlineKeyboard(post.HasTranslate, post.URLButtonText, post.URLButtonURL, post.EnableReactions, post.Reactions)
		editMessageReplyMarkup(token, cq.Message.Chat.ID, cq.Message.MessageID, newKeyboard)
		answerCallback(token, cq.ID, emoji, false)
		return
	}

	// من هذه النقطة فصاعداً، كل الأزرار تخص قوائم إعداد المسودة الخاصة بالبوت،
	// لذا نحذف رسالة القائمة السابقة قبل المتابعة.
	deleteTelegramMessage(token, chatID, cq.Message.MessageID)

	draft, hasDraft := userDrafts[chatID]

	if data == "action_skip_caption" {
		if hasDraft {
			askSettingsMenu(token, chatID, draft)
		}
		answerCallback(token, cq.ID, "", false)
		return
	}

	if data == "action_cancel" {
		delete(userDrafts, chatID)
		sendTelegramMessage(token, chatID, "❌ تم إلغاء عملية النشر.", nil)
		answerCallback(token, cq.ID, "تم الإلغاء", false)
		return
	}

	if data == "cancel_pending" {
		if hasDraft {
			draft.PendingInput = ""
			askSettingsMenu(token, chatID, draft)
		}
		answerCallback(token, cq.ID, "", false)
		return
	}

	if !hasDraft {
		sendTelegramMessage(token, chatID, "⚠️ انتهت جلسة النشر، يرجى إعادة إرسال المحتوى.", nil)
		answerCallback(token, cq.ID, "", false)
		return
	}

	switch data {
	case "toggle_protect":
		draft.ProtectContent = !draft.ProtectContent
		askSettingsMenu(token, chatID, draft)
		answerCallback(token, cq.ID, "", false)
		return

	case "toggle_spoiler":
		draft.HasSpoiler = !draft.HasSpoiler
		askSettingsMenu(token, chatID, draft)
		answerCallback(token, cq.ID, "", false)
		return

	case "toggle_reactions":
		draft.EnableReactions = !draft.EnableReactions
		askSettingsMenu(token, chatID, draft)
		answerCallback(token, cq.ID, "", false)
		return

	case "set_signature":
		draft.PendingInput = "signature"
		msgID := sendTelegramMessage(token, chatID, "📝 أرسل الآن نص التوقيع الذي سيُضاف أسفل كل منشور:", cancelPendingKeyboard())
		draft.LastBotMessageID = msgID
		answerCallback(token, cq.ID, "", false)
		return

	case "set_urlbutton":
		draft.URLButtonText = ""
		draft.URLButtonURL = ""
		draft.PendingInput = "url_text"
		msgID := sendTelegramMessage(token, chatID, "🔗 أرسل نص الزر (مثال: زورونا الآن):", cancelPendingKeyboard())
		draft.LastBotMessageID = msgID
		answerCallback(token, cq.ID, "", false)
		return

	case "set_lang":
		draft.PendingInput = "lang"
		msgID := sendTelegramMessage(token, chatID, "🌐 أرسل رمز لغة الترجمة الهدف (مثال: en, fr, tr, ar):", cancelPendingKeyboard())
		draft.LastBotMessageID = msgID
		answerCallback(token, cq.ID, "", false)
		return

	case "set_autodelete":
		draft.PendingInput = "autodelete"
		msgID := sendTelegramMessage(token, chatID, "⏱️ أرسل عدد الدقائق لحذف المنشور تلقائياً من القناة بعد نشره (أرسل 0 لإلغاء الحذف التلقائي):", cancelPendingKeyboard())
		draft.LastBotMessageID = msgID
		answerCallback(token, cq.ID, "", false)
		return

	case "confirm_publish":
		askSelectChannel(token, chatID, "اختر القناة التي تريد النشر فيها:")
		answerCallback(token, cq.ID, "", false)
		return
	}

	if strings.HasPrefix(data, "pub_channel_") {
		idxStr := strings.TrimPrefix(data, "pub_channel_")
		var idx int
		fmt.Sscanf(idxStr, "%d", &idx)

		channels := userChannels[chatID]
		if idx < 0 || idx >= len(channels) {
			sendTelegramMessage(token, chatID, "⚠️ خطأ في اختيار القناة.", nil)
			return
		}

		targetChannel := channels[idx]

		var publishedIDs []int
		var actualChatID int64

		switch {
		case len(draft.Media) > 1:
			ids, cid := publishMediaGroupToChannel(token, targetChannel.ID, draft)
			publishedIDs = ids
			actualChatID = cid

		case len(draft.Media) == 1:
			item := draft.Media[0]
			var id int
			var cid int64
			if item.Type == "photo" {
				id, cid = publishPhotoToChannel(token, targetChannel.ID, item.FileID, draft)
			} else if item.Type == "video" {
				id, cid = publishVideoToChannel(token, targetChannel.ID, item.FileID, draft)
			}
			if id != 0 {
				publishedIDs = append(publishedIDs, id)
			}
			actualChatID = cid

		default:
			id, cid := publishTextToChannel(token, targetChannel.ID, draft)
			if id != 0 {
				publishedIDs = append(publishedIDs, id)
			}
			actualChatID = cid
		}

		if draft.AutoDeleteDuration > 0 && actualChatID != 0 {
			for _, id := range publishedIDs {
				msgID := id
				cid := actualChatID
				delay := draft.AutoDeleteDuration
				time.AfterFunc(delay, func() {
					deleteTelegramMessage(token, cid, msgID)
				})
			}
		}

		delete(userDrafts, chatID)
		sendTelegramMessage(token, chatID, fmt.Sprintf("🚀 تم النشر في قناة (*%s*) بنجاح!", targetChannel.Title), nil)
		answerCallback(token, cq.ID, "تم النشر بنجاح!", false)
	}
}

// askSettingsMenu يعرض قائمة الإعدادات المتقدمة الكاملة للمسودة الحالية.
func askSettingsMenu(token string, chatID int64, draft *Draft) {
	summary := buildSettingsSummary(draft)

	buttons := [][]map[string]interface{}{
		{{"text": "📝 إضافة توقيع", "callback_data": "set_signature", "style": "primary"}},
		{{"text": fmt.Sprintf("🔒 حماية المحتوى: %s", boolLabel(draft.ProtectContent)), "callback_data": "toggle_protect", "style": toggleStyle(draft.ProtectContent)}},
		{{"text": fmt.Sprintf("👁️ تغبيش المحتوى: %s", boolLabel(draft.HasSpoiler)), "callback_data": "toggle_spoiler", "style": toggleStyle(draft.HasSpoiler)}},
		{{"text": fmt.Sprintf("👍 أزرار التفاعل: %s", boolLabel(draft.EnableReactions)), "callback_data": "toggle_reactions", "style": toggleStyle(draft.EnableReactions)}},
		{{"text": "🔗 زر رابط مخصص", "callback_data": "set_urlbutton", "style": "primary"}},
		{{"text": "🌐 تغيير لغة الترجمة", "callback_data": "set_lang", "style": "primary"}},
		{{"text": "⏱️ حذف تلقائي بعد فترة", "callback_data": "set_autodelete", "style": "primary"}},
		{{"text": "🚀 تأكيد النشر الان", "callback_data": "confirm_publish", "style": "success"}},
		{{"text": "❌ إلغاء", "callback_data": "action_cancel", "style": "danger"}},
	}

	msgID := sendTelegramMessage(token, chatID, summary, buttons)
	draft.LastBotMessageID = msgID
}

func buildSettingsSummary(draft *Draft) string {
	var sb strings.Builder
	sb.WriteString("⚙️ *إعدادات النشر المتقدمة*\n\n")

	if draft.Caption != "" {
		sb.WriteString("📝 النص الحالي:\n" + draft.Caption + "\n\n")
	} else {
		sb.WriteString("📝 لا يوجد نص (كابشن) حالياً.\n\n")
	}

	if draft.Signature != "" {
		sb.WriteString("🖊️ التوقيع: " + draft.Signature + "\n")
	}
	if draft.URLButtonText != "" && draft.URLButtonURL != "" {
		sb.WriteString(fmt.Sprintf("🔗 زر مخصص: %s ⬅️ %s\n", draft.URLButtonText, draft.URLButtonURL))
	}
	sb.WriteString(fmt.Sprintf("🌐 لغة الترجمة الافتراضية: %s\n", draft.TargetLang))
	if draft.AutoDeleteDuration > 0 {
		sb.WriteString(fmt.Sprintf("⏱️ حذف تلقائي بعد: %.0f دقيقة\n", draft.AutoDeleteDuration.Minutes()))
	}

	sb.WriteString("\nاختر أحد الخيارات لتعديل الإعدادات، أو اضغط 🚀 لاختيار القناة والنشر:")
	return sb.String()
}

func boolLabel(b bool) string {
	if b {
		return "✅ مفعّل"
	}
	return "❌ معطّل"
}

func toggleStyle(b bool) string {
	if b {
		return "success"
	}
	return "secondary"
}

func askSelectChannel(token string, chatID int64, promptMsg string) {
	channels := userChannels[chatID]
	if len(channels) == 0 {
		noChannelMsg := "⚠️ لم تقم بإضافة أي قناة بعد!\n\n" +
			"يرجى إرسال معرف القناة العامة مع الـ (@) أولاً.\n\n" +
			"وأذا كانت قناتك خاصة ارسل ايدي القناة (-100xxxx)\n" +
			"اذا كانت قناتك خاصة استخدم هذا البوت @UsernameToId_roBot عبر الضغط على kanal وتحديد قناتك وأرسالها الى البوت"
		sendTelegramMessage(token, chatID, noChannelMsg, nil)
		return
	}

	var keyboard [][]map[string]interface{}
	for i, ch := range channels {
		styleColor := "primary"
		if i%2 == 1 {
			styleColor = "success"
		}

		button := map[string]interface{}{
			"text":          "📢 " + ch.Title,
			"callback_data": fmt.Sprintf("pub_channel_%d", i),
			"style":         styleColor,
		}
		keyboard = append(keyboard, []map[string]interface{}{button})
	}

	keyboard = append(keyboard, []map[string]interface{}{
		{"text": "❌ إلغاء", "callback_data": "action_cancel", "style": "danger"},
	})

	msgID := sendTelegramMessage(token, chatID, promptMsg, keyboard)
	if draft, ok := userDrafts[chatID]; ok {
		draft.LastBotMessageID = msgID
	}
}

func fetchChannelTitle(token, channelID string) (string, error) {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/getChat?chat_id=%s", token, url.QueryEscape(channelID))
	resp, err := httpClient.Get(apiURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result TelegramResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || !result.OK {
		return "", fmt.Errorf("channel not found")
	}

	return result.Result.Title, nil
}

// buildInlineKeyboard يبني صفوف الأزرار النهائية لأي منشور (زر الترجمة، الزر
// المخصص برابط خارجي، وصف أزرار التفاعل مع عرض العدادات إن وُجدت).
func buildInlineKeyboard(includeTranslate bool, urlText, urlURL string, enableReactions bool, reactions map[string]int) [][]map[string]interface{} {
	var rows [][]map[string]interface{}

	if includeTranslate {
		rows = append(rows, []map[string]interface{}{
			{"text": "Translate", "callback_data": "translate", "style": "primary"},
		})
	}

	if urlText != "" && urlURL != "" {
		rows = append(rows, []map[string]interface{}{
			{"text": urlText, "url": urlURL, "style": "primary"},
		})
	}

	if enableReactions {
		row := make([]map[string]interface{}, 0, len(reactionEmojis))
		for _, e := range reactionEmojis {
			label := e
			if reactions != nil {
				if c := reactions[e]; c > 0 {
					label = fmt.Sprintf("%s %d", e, c)
				}
			}
			row = append(row, map[string]interface{}{
				"text":          label,
				"callback_data": "react_" + e,
				"style":         "secondary",
			})
		}
		rows = append(rows, row)
	}

	return rows
}

// applySignature يلحق نص التوقيع (إن وُجد) بالنص أو الكابشن الأساسي.
func applySignature(base, signature string) string {
	if signature == "" {
		return base
	}
	if base == "" {
		return signature
	}
	return base + "\n\n" + signature
}

// registerPublishedPost يحفظ إعدادات المنشور المرتبطة برسالة معينة في قناة،
// لاستخدامها لاحقاً عند الترجمة أو التفاعل مع المنشور.
func registerPublishedPost(chatID int64, messageID int, draft *Draft, hasTranslate bool) {
	if chatID == 0 || messageID == 0 {
		return
	}
	key := fmt.Sprintf("%d_%d", chatID, messageID)
	publishedPosts[key] = &PublishedPost{
		TargetLang:      draft.TargetLang,
		HasTranslate:    hasTranslate,
		URLButtonText:   draft.URLButtonText,
		URLButtonURL:    draft.URLButtonURL,
		EnableReactions: draft.EnableReactions,
		Reactions:       make(map[string]int),
	}
}

type publishResult struct {
	OK     bool `json:"ok"`
	Result struct {
		MessageID int `json:"message_id"`
		Chat      struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	} `json:"result"`
}

func publishTextToChannel(token, channelID string, draft *Draft) (int, int64) {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)

	text := applySignature(draft.Caption, draft.Signature)

	payload := map[string]interface{}{
		"chat_id": channelID,
		"text":    text,
	}
	if draft.ProtectContent {
		payload["protect_content"] = true
	}

	keyboard := buildInlineKeyboard(true, draft.URLButtonText, draft.URLButtonURL, draft.EnableReactions, nil)
	payload["reply_markup"] = map[string]interface{}{"inline_keyboard": keyboard}

	jsonBody, _ := json.Marshal(payload)
	resp, err := httpClient.Post(apiURL, "application/json", bytes.NewBuffer(jsonBody))
	if err != nil {
		log.Printf("publishTextToChannel: request error: %v", err)
		return 0, 0
	}
	defer resp.Body.Close()

	var res publishResult
	json.NewDecoder(resp.Body).Decode(&res)

	if res.Result.MessageID != 0 {
		registerPublishedPost(res.Result.Chat.ID, res.Result.MessageID, draft, true)
	}

	return res.Result.MessageID, res.Result.Chat.ID
}

func publishPhotoToChannel(token, channelID, photoID string, draft *Draft) (int, int64) {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendPhoto", token)

	caption := applySignature(draft.Caption, draft.Signature)

	payload := map[string]interface{}{
		"chat_id": channelID,
		"photo":   photoID,
		"caption": caption,
	}
	if draft.ProtectContent {
		payload["protect_content"] = true
	}
	if draft.HasSpoiler {
		payload["has_spoiler"] = true
	}

	keyboard := buildInlineKeyboard(true, draft.URLButtonText, draft.URLButtonURL, draft.EnableReactions, nil)
	payload["reply_markup"] = map[string]interface{}{"inline_keyboard": keyboard}

	jsonBody, _ := json.Marshal(payload)
	resp, err := httpClient.Post(apiURL, "application/json", bytes.NewBuffer(jsonBody))
	if err != nil {
		log.Printf("publishPhotoToChannel: request error: %v", err)
		return 0, 0
	}
	defer resp.Body.Close()

	var res publishResult
	json.NewDecoder(resp.Body).Decode(&res)

	if res.Result.MessageID != 0 {
		registerPublishedPost(res.Result.Chat.ID, res.Result.MessageID, draft, true)
	}

	return res.Result.MessageID, res.Result.Chat.ID
}

func publishVideoToChannel(token, channelID, videoID string, draft *Draft) (int, int64) {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendVideo", token)

	caption := applySignature(draft.Caption, draft.Signature)

	payload := map[string]interface{}{
		"chat_id": channelID,
		"video":   videoID,
		"caption": caption,
	}
	if draft.ProtectContent {
		payload["protect_content"] = true
	}
	if draft.HasSpoiler {
		payload["has_spoiler"] = true
	}

	keyboard := buildInlineKeyboard(true, draft.URLButtonText, draft.URLButtonURL, draft.EnableReactions, nil)
	payload["reply_markup"] = map[string]interface{}{"inline_keyboard": keyboard}

	jsonBody, _ := json.Marshal(payload)
	resp, err := httpClient.Post(apiURL, "application/json", bytes.NewBuffer(jsonBody))
	if err != nil {
		log.Printf("publishVideoToChannel: request error: %v", err)
		return 0, 0
	}
	defer resp.Body.Close()

	var res publishResult
	json.NewDecoder(resp.Body).Decode(&res)

	if res.Result.MessageID != 0 {
		registerPublishedPost(res.Result.Chat.ID, res.Result.MessageID, draft, true)
	}

	return res.Result.MessageID, res.Result.Chat.ID
}

// publishMediaGroupToChannel ينشر ألبوم الوسائط، ثم يرسل رسالة متابعة تحمل
// أزرار الترجمة/الرابط المخصص/التفاعل عند الحاجة (تيليجرام لا يسمح بأزرار
// inline على عناصر sendMediaGroup مباشرة). يعيد كل معرفات الرسائل المنشورة
// ومعرف القناة الرقمي الفعلي.
func publishMediaGroupToChannel(token, channelID string, draft *Draft) ([]int, int64) {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMediaGroup", token)

	caption := applySignature(draft.Caption, draft.Signature)

	var mediaList []InputMedia
	for i, item := range draft.Media {
		m := InputMedia{
			Type:  item.Type,
			Media: item.FileID,
		}
		if draft.HasSpoiler {
			m.HasSpoiler = true
		}
		if i == 0 && caption != "" {
			m.Caption = caption
		}
		mediaList = append(mediaList, m)
	}

	payload := map[string]interface{}{
		"chat_id": channelID,
		"media":   mediaList,
	}
	if draft.ProtectContent {
		payload["protect_content"] = true
	}

	jsonBody, _ := json.Marshal(payload)
	resp, err := httpClient.Post(apiURL, "application/json", bytes.NewBuffer(jsonBody))
	if err != nil {
		log.Printf("publishMediaGroupToChannel: request error: %v", err)
		return nil, 0
	}
	defer resp.Body.Close()

	var groupRes struct {
		OK     bool `json:"ok"`
		Result []struct {
			MessageID int `json:"message_id"`
			Chat      struct {
				ID int64 `json:"id"`
			} `json:"chat"`
		} `json:"result"`
	}
	json.NewDecoder(resp.Body).Decode(&groupRes)

	var ids []int
	var chatID int64
	for _, item := range groupRes.Result {
		ids = append(ids, item.MessageID)
		if chatID == 0 {
			chatID = item.Chat.ID
		}
	}

	needsButtons := caption != "" || draft.EnableReactions || (draft.URLButtonText != "" && draft.URLButtonURL != "")
	if needsButtons && len(ids) > 0 {
		keyboard := buildInlineKeyboard(caption != "", draft.URLButtonText, draft.URLButtonURL, draft.EnableReactions, nil)
		btnPayload := map[string]interface{}{
			"chat_id":             channelID,
			"text":                "ㅤ",
			"reply_to_message_id": ids[0],
			"reply_markup":        map[string]interface{}{"inline_keyboard": keyboard},
		}
		btnJson, _ := json.Marshal(btnPayload)
		sendMsgURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
		btnResp, err := httpClient.Post(sendMsgURL, "application/json", bytes.NewBuffer(btnJson))
		if err == nil {
			defer btnResp.Body.Close()
			var btnRes publishResult
			json.NewDecoder(btnResp.Body).Decode(&btnRes)
			if btnRes.Result.MessageID != 0 {
				ids = append(ids, btnRes.Result.MessageID)
				if chatID == 0 {
					chatID = btnRes.Result.Chat.ID
				}
				registerPublishedPost(chatID, btnRes.Result.MessageID, draft, caption != "")
			}
		} else {
			log.Printf("publishMediaGroupToChannel: buttons message error: %v", err)
		}
	}

	return ids, chatID
}

func deleteTelegramMessage(token string, chatID int64, messageID int) {
	if messageID == 0 {
		return
	}
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/deleteMessage", token)
	payload := map[string]interface{}{
		"chat_id":    chatID,
		"message_id": messageID,
	}
	jsonBody, _ := json.Marshal(payload)
	httpClient.Post(apiURL, "application/json", bytes.NewBuffer(jsonBody))
}

func editMessageReplyMarkup(token string, chatID int64, messageID int, keyboard [][]map[string]interface{}) {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/editMessageReplyMarkup", token)
	payload := map[string]interface{}{
		"chat_id":      chatID,
		"message_id":   messageID,
		"reply_markup": map[string]interface{}{"inline_keyboard": keyboard},
	}
	jsonBody, _ := json.Marshal(payload)
	httpClient.Post(apiURL, "application/json", bytes.NewBuffer(jsonBody))
}

func sendTelegramMessage(token string, chatID int64, text string, keyboard [][]map[string]interface{}) int {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	payload := map[string]interface{}{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "Markdown",
	}

	if keyboard != nil {
		payload["reply_markup"] = map[string]interface{}{
			"inline_keyboard": keyboard,
		}
	}

	jsonBody, _ := json.Marshal(payload)
	resp, err := httpClient.Post(apiURL, "application/json", bytes.NewBuffer(jsonBody))
	if err != nil {
		return 0
	}
	defer resp.Body.Close()

	var res struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int `json:"message_id"`
		} `json:"result"`
	}
	json.NewDecoder(resp.Body).Decode(&res)
	return res.Result.MessageID
}

func answerCallback(token, callbackID, text string, showAlert bool) {
	runes := []rune(text)
	if len(runes) > 200 {
		text = string(runes[:197]) + "..."
	}

	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/answerCallbackQuery", token)
	payload := map[string]interface{}{
		"callback_query_id": callbackID,
		"text":              text,
		"show_alert":        showAlert,
	}
	jsonBody, _ := json.Marshal(payload)
	httpClient.Post(apiURL, "application/json", bytes.NewBuffer(jsonBody))
}

func cleanString(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "ㅤ", "")
	s = strings.ReplaceAll(s, "\u200b", "")
	s = strings.ReplaceAll(s, "\u2800", "")
	return strings.TrimSpace(s)
}

// translateText يترجم النص المُعطى إلى لغة الهدف المحددة عبر MyMemory API.
// تم إصلاح المشاكل التالية مقارنة بالنسخة السابقة:
//  1. إضافة مهلة زمنية (timeout) للطلب حتى لا يتعلق ولا يفشل بصمت.
//  2. اقتطاع النصوص الطويلة لأن MyMemory يرفض النصوص الأطول من ~500 حرف.
//  3. التحقق الفعلي من responseStatus بدل الاكتفاء بوجود نص مُترجم،
//     لأن MyMemory قد يعيد رسالة خطأ (تجاوز الحد اليومي) داخل translatedText نفسه.
//  4. تسجيل الأخطاء عبر log.Printf بحيث تظهر في لوجز Vercel لتشخيص أي عطل لاحقاً.
//  5. دعم لغة هدف قابلة للتخصيص لكل مسودة/منشور بدلاً من العربية فقط.
func translateText(text, targetLang string) string {
	clean := cleanString(text)
	if clean == "" {
		return "لا يوجد نص محدد للترجمة."
	}

	if targetLang == "" {
		targetLang = "ar"
	}

	// MyMemory يرفض/يقطع النصوص الطويلة، لذا نحدّها احتياطاً
	runes := []rune(clean)
	if len(runes) > 490 {
		clean = string(runes[:490])
	}

	// ملاحظة: يمكن إضافة "&de=your-email@example.com" لرفع الحد اليومي
	// من 5000 كلمة إلى ما يقارب 50000 كلمة لكل IP.
	apiURL := fmt.Sprintf(
		"https://api.mymemory.translated.net/get?q=%s&langpair=en|%s",
		url.QueryEscape(clean),
		url.QueryEscape(targetLang),
	)

	resp, err := httpClient.Get(apiURL)
	if err != nil {
		log.Printf("translateText: request error: %v", err)
		return "تعذرت الترجمة حالياً، حاول مرة أخرى."
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("translateText: read error: %v", err)
		return "تعذرت الترجمة حالياً، حاول مرة أخرى."
	}

	if resp.StatusCode != http.StatusOK {
		log.Printf("translateText: bad HTTP status %d, body: %s", resp.StatusCode, string(body))
		return "تعذرت الترجمة حالياً، حاول مرة أخرى."
	}

	var result struct {
		ResponseData struct {
			TranslatedText string `json:"translatedText"`
		} `json:"responseData"`
		ResponseStatus interface{} `json:"responseStatus"` // قد يأتي كرقم أو كنص حسب حالة الرد
	}

	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("translateText: json unmarshal error: %v, body: %s", err, string(body))
		return "تعذرت ترجمة النص."
	}

	statusOK := false
	switch v := result.ResponseStatus.(type) {
	case float64:
		statusOK = v == 200
	case string:
		statusOK = v == "200"
	}

	if !statusOK {
		log.Printf("translateText: mymemory returned non-200 status, body: %s", string(body))
		return "تعذرت ترجمة النص (قد يكون تم تجاوز الحد المسموح للترجمة اليوم)."
	}

	if result.ResponseData.TranslatedText != "" {
		return result.ResponseData.TranslatedText
	}

	return "تعذرت ترجمة النص."
}
