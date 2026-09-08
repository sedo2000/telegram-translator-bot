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

type InputMedia struct {
	Type    string `json:"type"`
	Media   string `json:"media"`
	Caption string `json:"caption,omitempty"`
}

type Channel struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type Draft struct {
	Media            []MediaItem
	MediaGroupID     string
	Caption          string
	LastBotMessageID int
}

var (
	userChannels = make(map[int64][]Channel)
	userDrafts   = make(map[int64]*Draft)

	// عميل HTTP مشترك بمهلة زمنية محددة، يُستخدم في كل الطلبات الخارجية
	httpClient = &http.Client{Timeout: 8 * time.Second}
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

			if hasDraft && len(draft.Media) > 0 && draft.Caption == "" {
				draft.Caption = text
				askConfirmation(token, chatID, "هل تريد نشر المحتوى مع النص كـ كابشن؟")
				w.WriteHeader(http.StatusOK)
				return
			}

			userDrafts[chatID] = &Draft{Caption: text}
			askSelectChannel(token, chatID, "اختر القناة التي تريد نشر النص فيها:")
		}
	}

	w.WriteHeader(http.StatusOK)
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

		translatedText := translateToArabic(textToTranslate)
		answerCallback(token, cq.ID, translatedText, true)
		return
	}

	deleteTelegramMessage(token, chatID, cq.Message.MessageID)

	if data == "action_skip_caption" {
		askConfirmation(token, chatID, "هل تريد النشر بدون نص؟")
		answerCallback(token, cq.ID, "", false)
		return
	}

	if data == "action_cancel" {
		delete(userDrafts, chatID)
		sendTelegramMessage(token, chatID, "❌ تم إلغاء عملية النشر.", nil)
		answerCallback(token, cq.ID, "تم الإلغاء", false)
		return
	}

	if data == "action_confirm_pub" {
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
		draft, exists := userDrafts[chatID]
		if !exists {
			sendTelegramMessage(token, chatID, "⚠️ انتهت جلسة النشر، يرجى إعادة إرسال المحتوى.", nil)
			return
		}

		if len(draft.Media) > 1 {
			publishMediaGroupToChannel(token, targetChannel.ID, draft.Media, draft.Caption)
		} else if len(draft.Media) == 1 {
			item := draft.Media[0]
			if item.Type == "photo" {
				publishPhotoToChannel(token, targetChannel.ID, item.FileID, draft.Caption)
			} else if item.Type == "video" {
				publishVideoToChannel(token, targetChannel.ID, item.FileID, draft.Caption)
			}
		} else {
			publishTextToChannel(token, targetChannel.ID, draft.Caption)
		}

		delete(userDrafts, chatID)
		sendTelegramMessage(token, chatID, fmt.Sprintf("🚀 تم النشر في قناة (*%s*) بنجاح!", targetChannel.Title), nil)
		answerCallback(token, cq.ID, "تم النشر بنجاح!", false)
	}
}

func askConfirmation(token string, chatID int64, promptMsg string) {
	buttons := [][]map[string]interface{}{
		{
			{"text": "✅ نعم (تأكيد النشر)", "callback_data": "action_confirm_pub", "style": "success"},
			{"text": "❌ إلغاء", "callback_data": "action_cancel", "style": "danger"},
		},
	}
	msgID := sendTelegramMessage(token, chatID, promptMsg, buttons)
	if draft, ok := userDrafts[chatID]; ok {
		draft.LastBotMessageID = msgID
	}
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
	httpClient.Post(apiURL, "application/json", bytes.NewBuffer(jsonBody))
}

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
	httpClient.Post(apiURL, "application/json", bytes.NewBuffer(jsonBody))
}

func publishVideoToChannel(token, channelID, videoID, caption string) {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendVideo", token)
	payload := map[string]interface{}{
		"chat_id": channelID,
		"video":   videoID,
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
	httpClient.Post(apiURL, "application/json", bytes.NewBuffer(jsonBody))
}

func publishMediaGroupToChannel(token, channelID string, mediaItems []MediaItem, caption string) {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMediaGroup", token)
	var mediaList []InputMedia
	for i, item := range mediaItems {
		m := InputMedia{
			Type:  item.Type,
			Media: item.FileID,
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
	jsonBody, _ := json.Marshal(payload)
	resp, err := httpClient.Post(apiURL, "application/json", bytes.NewBuffer(jsonBody))
	if err != nil {
		return
	}
	defer resp.Body.Close()

	var groupRes struct {
		OK     bool `json:"ok"`
		Result []struct {
			MessageID int `json:"message_id"`
		} `json:"result"`
	}
	json.NewDecoder(resp.Body).Decode(&groupRes)

	if caption != "" && len(groupRes.Result) > 0 {
		firstMsgID := groupRes.Result[0].MessageID
		btnPayload := map[string]interface{}{
			"chat_id":             channelID,
			"text":                "ㅤ",
			"reply_to_message_id": firstMsgID,
			"reply_markup": map[string]interface{}{
				"inline_keyboard": [][]map[string]interface{}{
					{
						{"text": "Translate", "callback_data": "translate", "style": "primary"},
					},
				},
			},
		}
		btnJson, _ := json.Marshal(btnPayload)
		sendMsgURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
		httpClient.Post(sendMsgURL, "application/json", bytes.NewBuffer(btnJson))
	}
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

// translateToArabic يترجم النص المُعطى إلى العربية عبر MyMemory API.
// تم إصلاح المشاكل التالية مقارنة بالنسخة السابقة:
//  1. إضافة مهلة زمنية (timeout) للطلب حتى لا يتعلق ولا يفشل بصمت.
//  2. اقتطاع النصوص الطويلة لأن MyMemory يرفض النصوص الأطول من ~500 حرف.
//  3. التحقق الفعلي من responseStatus بدل الاكتفاء بوجود نص مُترجم،
//     لأن MyMemory قد يعيد رسالة خطأ (تجاوز الحد اليومي) داخل translatedText نفسه.
//  4. تسجيل الأخطاء عبر log.Printf بحيث تظهر في لوجز Vercel لتشخيص أي عطل لاحقاً.
func translateToArabic(text string) string {
	clean := cleanString(text)
	if clean == "" {
		return "لا يوجد نص محدد للترجمة."
	}

	// MyMemory يرفض/يقطع النصوص الطويلة، لذا نحدّها احتياطاً
	runes := []rune(clean)
	if len(runes) > 490 {
		clean = string(runes[:490])
	}

	// ملاحظة: يمكن إضافة "&de=your-email@example.com" لرفع الحد اليومي
	// من 5000 كلمة إلى ما يقارب 50000 كلمة لكل IP.
	apiURL := fmt.Sprintf(
		"https://api.mymemory.translated.net/get?q=%s&langpair=en|ar",
		url.QueryEscape(clean),
	)

	resp, err := httpClient.Get(apiURL)
	if err != nil {
		log.Printf("translateToArabic: request error: %v", err)
		return "تعذرت الترجمة حالياً، حاول مرة أخرى."
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("translateToArabic: read error: %v", err)
		return "تعذرت الترجمة حالياً، حاول مرة أخرى."
	}

	if resp.StatusCode != http.StatusOK {
		log.Printf("translateToArabic: bad HTTP status %d, body: %s", resp.StatusCode, string(body))
		return "تعذرت الترجمة حالياً، حاول مرة أخرى."
	}

	var result struct {
		ResponseData struct {
			TranslatedText string `json:"translatedText"`
		} `json:"responseData"`
		ResponseStatus interface{} `json:"responseStatus"` // قد يأتي كرقم أو كنص حسب حالة الرد
	}

	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("translateToArabic: json unmarshal error: %v, body: %s", err, string(body))
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
		log.Printf("translateToArabic: mymemory returned non-200 status, body: %s", string(body))
		return "تعذرت ترجمة النص (قد يكون تم تجاوز الحد المسموح للترجمة اليوم)."
	}

	if result.ResponseData.TranslatedText != "" {
		return result.ResponseData.TranslatedText
	}

	return "تعذرت ترجمة النص."
}
