package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// هياكل تليجرام
type Update struct {
	UpdateID      int            `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

type Message struct {
	MessageID int    `json:"message_id"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	Data    string   `json:"data"`
	Message *Message `json:"message"` // للحصول على النص الأصلي للرسالة
}

// ذاكرة عشوائية لحفظ القنوات لكل مستخدم بدون قاعدة بيانات
// ملاحظة: بما أن Vercel تعمل بنظام Serverless، قد يتم تفريغ هذه الذاكرة إذا توقف البوت عن العمل لفترة
var userChannels = make(map[int64]string)

// دالة الويب هوك الرئيسية
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

	// 1. التعامل مع الضغط على زر الترجمة
	if update.CallbackQuery != nil {
		handleCallbackQuery(token, update.CallbackQuery)
		w.WriteHeader(http.StatusOK)
		return
	}

	// 2. التعامل مع الرسائل الواردة
	if update.Message != nil && update.Message.Text != "" {
		text := update.Message.Text
		chatID := update.Message.Chat.ID

		// أ. رسالة الترحيب
		if text == "/start" {
			welcomeMsg := "مرحباً بك في بوت النشر والترجمة! 🤖\n\n" +
				"أولاً: قم برفع البوت كمشرف (Admin) في قناتك مع صلاحيات (نشر الرسائل).\n" +
				"ثانياً: أرسل لي معرف القناة:\n" +
				"- إذا كانت عامة: أرسل المعرف مسبوقاً بـ @ (مثال: @MyChannel)\n" +
				"- إذا كانت خاصة: أرسل رابط الدعوة الخاص بها لنتعرف عليها."
			sendTelegramMessage(token, chatID, welcomeMsg)
			w.WriteHeader(http.StatusOK)
			return
		}

		// ب. التعرف على القناة العامة
		if strings.HasPrefix(text, "@") {
			userChannels[chatID] = text // حفظ القناة في الذاكرة
			sendTelegramMessage(token, chatID, "✅ تم حفظ القناة العامة بنجاح ("+text+").\nالآن أرسل لي أي نص باللغة الإنجليزية وسأقوم بنشره فوراً!")
			w.WriteHeader(http.StatusOK)
			return
		}

		// ج. التعرف على رابط القناة الخاصة
		if strings.HasPrefix(text, "https://t.me/+") {
			reply := "🔗 يبدو أن هذه قناة خاصة.\nالبوت لا يمكنه النشر عبر الرابط، يرجى إرسال الـ ID الخاص بالقناة (غالباً يبدأ بـ -100).\n\n*تلميح:* للحصول على الايدي، قم بتوجيه رسالة من قناتك الخاصة إلى بوت @userinfobot ثم انسخ الايدي وأرسله هنا."
			sendTelegramMessage(token, chatID, reply)
			w.WriteHeader(http.StatusOK)
			return
		}

		// د. التعرف على الايدي الخاص بالقناة
		if strings.HasPrefix(text, "-100") {
			userChannels[chatID] = text // حفظ القناة في الذاكرة
			sendTelegramMessage(token, chatID, "✅ تم حفظ القناة الخاصة بنجاح ("+text+").\nالآن أرسل لي أي نص وسأقوم بنشره!")
			w.WriteHeader(http.StatusOK)
			return
		}

		// هـ. نشر النص في القناة المربوطة
		channel, exists := userChannels[chatID]
		if !exists {
			sendTelegramMessage(token, chatID, "⚠️ لم تقم بتحديد القناة بعد!\nيرجى إرسال معرف القناة (@) أو الايدي (-100) أولاً.")
			w.WriteHeader(http.StatusOK)
			return
		}

		// النشر في القناة
		publishToChannel(token, channel, text)
		sendTelegramMessage(token, chatID, "🚀 تم النشر في القناة بنجاح!")
	}

	w.WriteHeader(http.StatusOK)
}

// دالة النشر مع الزر
func publishToChannel(token, channel, text string) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)

	payload := map[string]interface{}{
		"chat_id": channel,
		"text":    text,
		"reply_markup": map[string]interface{}{
			"inline_keyboard": [][]map[string]interface{}{
				{
					{
						"text":          "Translate", 
						"callback_data": "translate", // لا نرسل النص هنا لكي لا نتجاوز حد الـ 64 بايت
					},
				},
			},
		},
	}

	jsonBody, _ := json.Marshal(payload)
	http.Post(url, "application/json", bytes.NewBuffer(jsonBody))
}

// دالة التعامل مع الضغط على زر الترجمة
func handleCallbackQuery(token string, cq *CallbackQuery) {
	if cq.Data == "translate" {
		// نجلب النص الأصلي من الرسالة التي يحتوي عليها الزر! (حل ذكي لمنع استخدام قواعد البيانات)
		originalText := cq.Message.Text 

		// نقوم بترجمة النص
		translatedText := translateToArabic(originalText)

		// نظهر النافذة المنبثقة Alert
		// زر "حسنًا" يضاف تلقائياً من تليجرام عندما يكون show_alert: true
		url := fmt.Sprintf("https://api.telegram.org/bot%s/answerCallbackQuery", token)
		payload := map[string]interface{}{
			"callback_query_id": cq.ID,
			"text":              translatedText,
			"show_alert":        true, 
		}

		jsonBody, _ := json.Marshal(payload)
		http.Post(url, "application/json", bytes.NewBuffer(jsonBody))
	}
}

// دالة وهمية للترجمة (يجب ربطها بـ API حقيقي مثل Google Translate)
func translateToArabic(text string) string {
	// كمثال توضيحي: سيقوم البوت بطباعة النص كأنه تمت ترجمته
	return "الترجمة العربية:\n" + text + "\n\n(تمت الترجمة بنجاح)"
}

// إرسال رسائل نصية عادية للمستخدم
func sendTelegramMessage(token string, chatID int64, text string) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	payload := map[string]interface{}{
		"chat_id": chatID,
		"text":    text,
	}
	jsonBody, _ := json.Marshal(payload)
	http.Post(url, "application/json", bytes.NewBuffer(jsonBody))
}
