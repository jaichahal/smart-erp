package periods

// Messages shown to callers. Accept-Language selects the language; data is never translated.
var text = map[string]map[string]string{
	"en": {
		"period_closed":            "This period is closed.",
		"period_backdate_denied":   "Posting into a prior open period requires the back-dating permission.",
		"period_soft_denied":       "Soft close is available to the Accountant role only.",
		"period_hard_denied":       "Hard close is available to the Stakeholder role only.",
		"period_approval_required": "Hard close requires an approval.",
		"period_approval_missing":  "An approval id is required.",
		"period_not_found":         "Period not found.",
		"period_conflict":          "The period changed since it was read.",
		"company_invalid":          "Company name is required.",
		"year_invalid":             "Fiscal year is not valid.",
		"doc_type_invalid":         "Document type is not valid.",
		"registration_invalid":     "Registration is missing a document or fiscal year.",
		"approval_unconfigured":    "Approvals are not configured.",
		"outbox_unconfigured":      "The outbox is not configured.",
		"auth_required":            "Authentication is required.",
		"actor_system":             "System",
		"reason_backdated":         "Posted into a prior open period.",
	},
	"ar": {
		"period_closed":            "هذه الفترة مغلقة.",
		"period_backdate_denied":   "الترحيل إلى فترة سابقة مفتوحة يتطلب صلاحية الترحيل التاريخي.",
		"period_soft_denied":       "الإقفال المبدئي متاح لدور المحاسب فقط.",
		"period_hard_denied":       "الإقفال النهائي متاح لدور صاحب المصلحة فقط.",
		"period_approval_required": "الإقفال النهائي يتطلب موافقة.",
		"period_approval_missing":  "معرّف الموافقة مطلوب.",
		"period_not_found":         "الفترة غير موجودة.",
		"period_conflict":          "تغيرت الفترة منذ قراءتها.",
		"company_invalid":          "اسم الشركة مطلوب.",
		"year_invalid":             "السنة المالية غير صالحة.",
		"doc_type_invalid":         "نوع المستند غير صالح.",
		"registration_invalid":     "التسجيل يفتقد المستند أو السنة المالية.",
		"approval_unconfigured":    "الموافقات غير مهيأة.",
		"outbox_unconfigured":      "صندوق الأحداث غير مهيأ.",
		"auth_required":            "المصادقة مطلوبة.",
		"actor_system":             "النظام",
		"reason_backdated":         "رُحّل إلى فترة سابقة مفتوحة.",
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
