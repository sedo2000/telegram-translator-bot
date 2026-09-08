package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// هياكل بيانات تليجرام (Telegram Update Structures)
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
	Message *Message `json:"message"`
}

// الدالة الرئيسية التي تستقبل طلبات الـ Webhook على Vercel
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
	targetChannel := os.Getenv("TARGET_CHANNEL") // معرف أو معرف معرف القناة مثل @channelName أو -100xxxxxxxxxx

	// 1. التعامل مع الضغط على زر الترجمة (Callback Query)
	if update.CallbackQuery != nil {
		handleCallbackQuery(token, update.CallbackQuery)
		w.WriteHeader(http.StatusOK)
		return
	}

	// 2. التعامل مع الرسائل الواردة إلى البوت
	if update.Message != nil && update.Message.Text != "" {
		text := update.Message.Text

		// أمر تعريفي لتحديد القناة أو التعامل مع الأوامر
		if strings.HasPrefix(text, "/setchannel") {
			parts := strings.SplitN(text, " ", 2)
			if len(parts) == 2 {
				// يمكنك حفظ القناة في قاعدة بيانات، هنا كمثال نرسل تأكيد للمستخدم
				sendTelegramMessage(token, update.Message.Chat.ID, "تم استقبال القناة: "+parts[1])
			}
			w.WriteHeader(http.StatusOK)
			return
		}

		// نشر النص المرسل إلى القناة العامة أو الخاصة مع زر Translate
		publishToChannel(token, targetChannel, text)
	}

	w.WriteHeader(http.StatusOK)
}

// دالة نشر الرسالة في القناة مع زر الترجمة
func publishToChannel(token, channel, text string) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)

	// إعداد زر الانلاين المكتوب عليه Translate
	payload := map[string]interface{}{
		"chat_id": channel,
		"text":    text,
		"reply_markup": map[string]interface{}{
			"inline_keyboard": [][]map[string]interface{}{
				{
					{
						"text":          "Translate",
						"callback_data": "translate_" + text, // ملاحظة: يجب مراعاة حدود 64 بايت لـ callback_data في النصوص الطويلة أو تخزين النص في قاعدة بيانات
					},
				},
			},
		},
	}

	jsonBody, _ := json.Marshal(payload)
	http.Post(url, "application/json", bytes.NewBuffer(jsonBody))
}

// دالة الرد عند الضغط على زر الترجمة وإظهار النافذة المنبثقة (Alert Modal مع زر حسنًا)
func handleCallbackQuery(token string, cq *CallbackQuery) {
	textToTranslate := strings.TrimPrefix(cq.Data, "translate_")

	// تنفيذ الترجمة (يمكنك ربطها بأي API ترجمة مثل Google Translate أو LibreTranslate)
	translatedText := translateToArabic(textToTranslate)

	// استخدام show_alert: true لفتح النافذة المنبثقة (Modal) التي تحتوي على زر "حسنًا" تلقائياً من تليجرام
	url := fmt.Sprintf("https://api.telegram.org/bot%s/answerCallbackQuery", token)
	payload := map[string]interface{}{
		"callback_query_id": cq.ID,
		"text":              translatedText,
		"show_alert":        true, // هذا الخيار يظهر نافذة منبثقة مع زر "حسنًا" كما في صورتك الثانية
	}

	jsonBody, _ := json.Marshal(payload)
	http.Post(url, "application/json", bytes.NewBuffer(jsonBody))
}

// دالة وهمية للترجمة (استبدلها بخدمة ترجمة حقيقية)
func translateToArabic(text string) string {
	// ضع هنا كود الاتصال بخدمة الترجمة
	return "الترجمة العربية: " + text
}

// دالة إرسال رسالة عادية للمستخدم
func sendTelegramMessage(token string, chatID int64, text string) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	payload := map[string]interface{}{
		"chat_id": chatID,
		"text":    text,
	}
	jsonBody, _ := json.Marshal(payload)
	http.Post(url, "application/json", bytes.NewBuffer(jsonBody))
}
