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

var userChannels = make(map[int64]string)

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

		if text == "/start" {
			welcomeMsg := "مرحباً بك في بوت النشر والترجمة! 🤖\n\n" +
				"أولاً: قم برفع البوت كمشرف (Admin) في قناتك مع صلاحيات (نشر الرسائل).\n" +
				"ثانياً: أرسل لي معرف القناة:\n" +
				"- إذا كانت عامة: أرسل المعرف مسبوقاً بـ @ (مثال: @MyChannel)\n" +
				"- إذا كانت خاصة: أرسل الايدي الخاص بها (مثال: -100123456789)"
			sendTelegramMessage(token, chatID, welcomeMsg)
			w.WriteHeader(http.StatusOK)
			return
		}

		if strings.HasPrefix(text, "@") {
			userChannels[chatID] = text
			sendTelegramMessage(token, chatID, "✅ تم حفظ القناة العامة بنجاح ("+text+").\nأرسل لي الآن أي نص ونشره في القناة!")
			w.WriteHeader(http.StatusOK)
			return
		}

		if strings.HasPrefix(text, "https://t.me/+") {
			reply := "🔗 هذه قناة خاصة.\nيرجى إرسال الـ ID الخاص بالقناة بدلاً من الرابط (يبدأ بـ -100)."
			sendTelegramMessage(token, chatID, reply)
			w.WriteHeader(http.StatusOK)
			return
		}

		if strings.HasPrefix(text, "-100") {
			userChannels[chatID] = text
			sendTelegramMessage(token, chatID, "✅ تم حفظ القناة الخاصة بنجاح ("+text+").\nأرسل لي الآن أي نص ونشره في القناة!")
			w.WriteHeader(http.StatusOK)
			return
		}

		channel, exists := userChannels[chatID]
		if !exists {
			sendTelegramMessage(token, chatID, "⚠️ لم تقم بتحديد القناة بعد!\nيرجى إرسال معرف القناة (@) أو الايدي (-100) أولاً.")
			w.WriteHeader(http.StatusOK)
			return
		}

		publishToChannel(token, channel, text)
		sendTelegramMessage(token, chatID, "🚀 تم النشر في القناة بنجاح!")
	}

	w.WriteHeader(http.StatusOK)
}

func publishToChannel(token, channel, text string) {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)

	payload := map[string]interface{}{
		"chat_id": channel,
		"text":    text,
		"reply_markup": map[string]interface{}{
			"inline_keyboard": [][]map[string]interface{}{
				{
					{
						"text":          "Translate",
						"callback_data": "translate",
						"style":         "primary", // تلوين الزر باللون الأزرق (Primary) حسب الشرح
					},
				},
			},
		},
	}

	jsonBody, _ := json.Marshal(payload)
	http.Post(apiURL, "application/json", bytes.NewBuffer(jsonBody))
}

func handleCallbackQuery(token string, cq *CallbackQuery) {
	if cq.Data == "translate" {
		originalText := cq.Message.Text

		// ترجمة النص الأصلي تلقائياً إلى العربية من أي لغة
		translatedText := translateToArabic(originalText)

		apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/answerCallbackQuery", token)
		payload := map[string]interface{}{
			"callback_query_id": cq.ID,
			"text":              translatedText, // إظهار النص المترجم فقط
			"show_alert":        true,
		}

		jsonBody, _ := json.Marshal(payload)
		http.Post(apiURL, "application/json", bytes.NewBuffer(jsonBody))
	}
}

// دالة الترجمة إلى اللغة العربية من أي لغة تلقائياً
func translateToArabic(text string) string {
	apiURL := fmt.Sprintf("https://translate.googleapis.com/translate_a/single?client=gtx&sl=auto&tl=ar&dt=t&q=%s", url.QueryEscape(text))

	resp, err := http.Get(apiURL)
	if err != nil {
		return "حدث خطأ في الاتصال بخدمة الترجمة"
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "خطأ في قراءة بيانات الترجمة"
	}

	var result []interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return "خطأ في معالجة الترجمة"
	}

	if len(result) > 0 {
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
	}

	return "تعذرت ترجمة هذا النص"
}

func sendTelegramMessage(token string, chatID int64, text string) {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	payload := map[string]interface{}{
		"chat_id": chatID,
		"text":    text,
	}
	jsonBody, _ := json.Marshal(payload)
	http.Post(apiURL, "application/json", bytes.NewBuffer(jsonBody))
}
