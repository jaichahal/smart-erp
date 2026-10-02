package clocks

var text = map[string]map[string]string{
	"en": {
		"calendar_invalid":      "The holiday calendar is not valid.",
		"calendar_missing":      "The holiday calendar is not set.",
		"calendar_conflict":     "The holiday calendar changed since it was read.",
		"clock_invalid":         "The clock is not valid.",
		"clock_no_business_day": "The calendar has no working day inside the window.",
		"outbox_unconfigured":   "The outbox is not configured.",
		"auth_required":         "Authentication is required.",
		"actor_system":          "System",
	},
	"ar": {
		"calendar_invalid":      "تقويم العطل غير صالح.",
		"calendar_missing":      "تقويم العطل غير مُعد.",
		"calendar_conflict":     "تغير تقويم العطل منذ قراءته.",
		"clock_invalid":         "المؤقت غير صالح.",
		"clock_no_business_day": "لا يوجد يوم عمل في التقويم ضمن النافذة.",
		"outbox_unconfigured":   "صندوق الأحداث غير مهيأ.",
		"auth_required":         "المصادقة مطلوبة.",
		"actor_system":          "النظام",
	},
}

func tr(lang, key string) string {
	if lang != "ar" {
		lang = "en"
	}
	if msg, ok := text[lang][key]; ok {
		return msg
	}
	if msg, ok := text["en"][key]; ok {
		return msg
	}
	return key
}
