package notifications

import "strings"

// catalog holds user-facing copy. Handlers pass a key to text; they do not
// embed the sentence. Accept-Language selects en or ar (04 transport).
var catalog = map[string]map[string]string{
	"en": {
		"auth.required":         "Authentication is required",
		"validation.event":      "Event payload is invalid",
		"validation.amount":     "An amount requires a currency",
		"validation.push":       "Push messages are data-only",
		"validation.token":      "Push token is invalid",
		"validation.platform":   "Platform must be android, ios, or console",
		"validation.device":     "A device id is required",
		"validation.rule":       "Alert rule is invalid",
		"conflict.version":      "The document changed; reload it before approving",
		"alert.blocking":        "A blocking alert rule prevented submission",
		"notfound.notification": "Notification was not found",
	},
	"ar": {
		"auth.required":         "يلزم تسجيل الدخول",
		"validation.event":      "بيانات الحدث غير صالحة",
		"validation.amount":     "المبلغ يحتاج إلى عملة",
		"validation.push":       "رسائل الدفع بيانات فقط",
		"validation.token":      "رمز الدفع غير صالح",
		"validation.platform":   "المنصة يجب أن تكون أندرويد أو آيفون أو وحدة التحكم",
		"validation.device":     "معرّف الجهاز مطلوب",
		"validation.rule":       "قاعدة التنبيه غير صالحة",
		"conflict.version":      "تغيّر المستند؛ أعد تحميله قبل الموافقة",
		"alert.blocking":        "قاعدة تنبيه مانعة منعت الإرسال",
		"notfound.notification": "الإشعار غير موجود",
	},
}

func text(lang, key string) string {
	if !strings.HasPrefix(strings.ToLower(lang), "ar") {
		lang = "en"
	} else {
		lang = "ar"
	}
	if msg, ok := catalog[lang][key]; ok && msg != "" {
		return msg
	}
	return catalog["en"][key]
}
