package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// هياكل بيانات تليجرام
type Update struct {
	UpdateID      int            `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

type Message struct {
	MessageID int         `json:"message_id"`
	Chat      Chat        `json:"chat"`
	Text      string      `json:"text"`
	Photo     []PhotoSize `json:"photo"`
	Caption   string      `json:"caption"`
}

type PhotoSize struct {
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

type User struct {
	ID int64 `json:"id"`
}

type TelegramResponse struct {
	OK     bool `json:"ok"`
	Result Chat `json:"result"`
}

// هيكل القناة المحفوظة
type Channel struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// هيكل المسودة الحالية للنشر
type Draft struct {
	PhotoID string
	Caption string
}

// الذاكرة المؤقتة لتدفق العمل القنوات والمسودات
var (
	userChannels = make(map[int64][]Channel)
	userDrafts   = make(map[int64]*Draft)
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

	// 1. التعامل مع الضغط على الأزرار الشفافة (Callback Query)
	if update.CallbackQuery != nil {
		handleCallbackQuery(token, update.CallbackQuery)
		w.WriteHeader(http.StatusOK)
		return
	}

	// 2. التعامل مع الرسائل الواردة (نصوص أو صور)
	if update.Message != nil {
		chatID := update.Message.Chat.ID
		text := update.Message.Text

		// أمر البداية /start
		if text == "/start" {
			welcomeMsg := "مرحباً بك في بوت النشر والترجمة المتقدم! 🤖\n\n" +
				"1️⃣ قم برفع البوت كمشرف (Admin) في قناتك مع صلاحيات نشر الرسائل.\n" +
				"2️⃣ أرسل معرف القناة (@Channel) أو ايدي القناة الخاصة (-100xxxx) لإضافتها لملفك.\n" +
				"3️⃣ يمكنك إرسال عدة قنوات وسيظهر لك البوت قائمة بأسمائها عند كل عملية نشر!\n" +
				"4️⃣ يمكنك إرسال نصوص أو صور للنشر بشكل مباشر."
			sendTelegramMessage(token, chatID, welcomeMsg, nil)
			w.WriteHeader(http.StatusOK)
			return
		}

		// إضافة قناة جديدة (عن طريق المعرف أو ID)
		if strings.HasPrefix(text, "@") || strings.HasPrefix(text, "-100") {
			chTitle, err := fetchChannelTitle(token, text)
			if err != nil {
				sendTelegramMessage(token, chatID, "⚠️ تعذر الوصول للقناة! تأكد من رفع البوت مشرفاً فيها وأعد المحاولة.", nil)
			} else {
				// إضافة القناة للقائمة الخاصة بالمستخدم
				userChannels[chatID] = append(userChannels[chatID], Channel{ID: text, Title: chTitle})
				sendTelegramMessage(token, chatID, fmt.Sprintf("✅ تم إضافة القناة بنجاح:\n📌 *%s*", chTitle), nil)
			}
			w.WriteHeader(http.StatusOK)
			return
		}

		// التعامل مع رفع الصورة
		if len(update.Message.Photo) > 0 {
			// جلب أعلى دقة للصورة
			photoID := update.Message.Photo[len(update.Message.Photo)-1].FileID
			userDrafts[chatID] = &Draft{PhotoID: photoID}

			// عرض أزرار التخطي أو الإلغاء
			buttons := [][]map[string]interface{}{
				{
					{"text": "⏩ تخطي (بدون نص)", "callback_data": "action_skip_caption", "style": "primary"},
					{"text": "❌ إلغاء", "callback_data": "action_cancel", "style": "danger"},
				},
			}
			sendTelegramMessage(token, chatID, "📸 تم استلام الصورة!\nهل تريد إضافة نص (كابشن) أسفل الصورة؟\n\n- أرسل النص الآن كرسالة عادية.\n- أو اضغط على (تخطي) للنشر بدون نص.", buttons)
			w.WriteHeader(http.StatusOK)
			return
		}

		// التعامل مع النصوص
		if text != "" {
			draft, hasDraft := userDrafts[chatID]

			// إذا كان المستخدم قد أرسل صورة وينتظر إضافة كابشن لها
			if hasDraft && draft.PhotoID != "" && draft.Caption == "" {
				draft.Caption = text
				askConfirmation(token, chatID, "هل تريد نشر الصورة مع النص كـ كابشن؟")
				w.WriteHeader(http.StatusOK)
				return
			}

			// نص عادي بدون صورة -> حفظ النص في المسودة وعرض القنوات
			userDrafts[chatID] = &Draft{Caption: text}
			askSelectChannel(token, chatID, "اختر القناة التي تريد نشر النص فيها:")
		}
	}

	w.WriteHeader(http.StatusOK)
}

// دالة التعامل مع الضغط على الأزرار
func handleCallbackQuery(token string, cq *CallbackQuery) {
	chatID := cq.From.ID
	data := cq.Data

	// أ. ترجمة النص عند ضغط المتابع في القناة
	if data == "translate" {
		textToTranslate := cq.Message.Text
		if textToTranslate == "" {
			textToTranslate = cq.Message.Caption // دعم ترجمة الكابشن الخاص بالصور
		}

		translatedText := translateToArabic(textToTranslate)
		answerCallback(token, cq.ID, translatedText, true)
		return
	}

	// ب. زر التخطي (عدم إضافة كابشن للصورة)
	if data == "action_skip_caption" {
		askConfirmation(token, chatID, "هل تريد النشر بدون نص؟")
		answerCallback(token, cq.ID, "", false)
		return
	}

	// ج. زر الإلغاء
	if data == "action_cancel" {
		delete(userDrafts, chatID)
		sendTelegramMessage(token, chatID, "❌ تم إلغاء عملية النشر.", nil)
		answerCallback(token, cq.ID, "تم الإلغاء", false)
		return
	}

	// د. زر نعم (تأكيد النشر)
	if data == "action_confirm_pub" {
		askSelectChannel(token, chatID, "اختر القناة التي تريد النشر فيها:")
		answerCallback(token, cq.ID, "", false)
		return
	}

	// هـ. اختيار قناة معينة والنشر فيها
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
		draft, exists := userDrafts[chatID]
		if !exists {
			sendTelegramMessage(token, chatID, "⚠️ انتهت جلسة النشر، يرجى إعادة إرسال المحتوى.", nil)
			return
		}

		// نشر الصورة أو النص
		if draft.PhotoID != "" {
			publishPhotoToChannel(token, targetChannel.ID, draft.PhotoID, draft.Caption)
		} else {
			publishTextToChannel(token, targetChannel.ID, draft.Caption)
		}

		delete(userDrafts, chatID)
		sendTelegramMessage(token, chatID, fmt.Sprintf("🚀 تم النشر في قناة (*%s*) بنجاح!", targetChannel.Title), nil)
		answerCallback(token, cq.ID, "تم النشر بنجاح!", false)
	}
}

// عرض أزرار التأكيد (نعم / إلغاء)
func askConfirmation(token string, chatID int64, promptMsg string) {
	buttons := [][]map[string]interface{}{
		{
			{"text": "✅ نعم (تأكيد النشر)", "callback_data": "action_confirm_pub", "style": "success"},
			{"text": "❌ إلغاء", "callback_data": "action_cancel", "style": "danger"},
		},
	}
	sendTelegramMessage(token, chatID, promptMsg, buttons)
}

// عرض قائمة القنوات المضافة للمستخدم كأزرار ملونة
func askSelectChannel(token string, chatID int64, promptMsg string) {
	channels := userChannels[chatID]
	if len(channels) == 0 {
		sendTelegramMessage(token, chatID, "⚠️ لم تقم بإضافة أي قناة بعد!\nيرجى إرسال معرف القناة (@Channel) أولاً.", nil)
		return
	}

	var keyboard [][]map[string]interface{}
	for i, ch := range channels {
		// التنويع في الألوان بين أزرق وأخضر للأزرار
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

	// إضافة زر إلغاء في الأسفل
	keyboard = append(keyboard, []map[string]interface{}{
		{"text": "❌ إلغاء", "callback_data": "action_cancel", "style": "danger"},
	})

	sendTelegramMessage(token, chatID, promptMsg, keyboard)
}

// جلب اسم القناة باستخدام Bot API
func fetchChannelTitle(token, channelID string) (string, error) {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/getChat?chat_id=%s", token, url.QueryEscape(channelID))
	resp, err := http.Get(apiURL)
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

// نشر نص في القناة مع زر الترجمة
func publishTextToChannel(token, channelID, text string) {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	payload := map[string]interface{}{
		"chat_id": channelID,
		"text":    text,
		"reply_markup": map[string]interface{}{
			"inline_keyboard": [][]map[string]interface{}{
				{
					{"text": "Translate", "callback_data": "translate", "style": "primary"},
				},
			},
		},
	}
	jsonBody, _ := json.Marshal(payload)
	http.Post(apiURL, "application/json", bytes.NewBuffer(jsonBody))
}

// نشر صورة مع كابشن وزر الترجمة
func publishPhotoToChannel(token, channelID, photoID, caption string) {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendPhoto", token)
	payload := map[string]interface{}{
		"chat_id": channelID,
		"photo":   photoID,
		"caption": caption,
		"reply_markup": map[string]interface{}{
			"inline_keyboard": [][]map[string]interface{}{
				{
					{"text": "Translate", "callback_data": "translate", "style": "primary"},
				},
			},
		},
	}
	jsonBody, _ := json.Marshal(payload)
	http.Post(apiURL, "application/json", bytes.NewBuffer(jsonBody))
}

// إرسال رسالة عادية أو مع أزرار
func sendTelegramMessage(token string, chatID int64, text string, keyboard [][]map[string]interface{}) {
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
	http.Post(apiURL, "application/json", bytes.NewBuffer(jsonBody))
}

// الإجابة على Callback Query
func answerCallback(token, callbackID, text string, showAlert bool) {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/answerCallbackQuery", token)
	payload := map[string]interface{}{
		"callback_query_id": callbackID,
		"text":              text,
		"show_alert":        showAlert,
	}
	jsonBody, _ := json.Marshal(payload)
	http.Post(apiURL, "application/json", bytes.NewBuffer(jsonBody))
}

// دالة الترجمة إلى العربية
func translateToArabic(text string) string {
	if text == "" {
		return "لا يوجد نص للترجمة."
	}
	apiURL := fmt.Sprintf("https://translate.googleapis.com/translate_a/single?client=gtx&sl=auto&tl=ar&dt=t&q=%s", url.QueryEscape(text))
	resp, err := http.Get(apiURL)
	if err != nil {
		return "حدث خطأ أثناء الاتصال بخدمة الترجمة."
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result []interface{}
	if err := json.Unmarshal(body, &result); err != nil || len(result) == 0 {
		return "تعذرت ترجمة النص."
	}

	if inner, ok := result[0].([]interface{}); ok {
		var fullTranslation string
		for _, elem := range inner {
			if item, ok := elem.([]interface{}); ok && len(item) > 0 {
				if translatedSegment, ok := item[0].(string); ok {
					fullTranslation += translatedSegment
				}
			}
		}
		if fullTranslation != "" {
			return fullTranslation
		}
	}

	return "تعذرت ترجمة النص."
}
