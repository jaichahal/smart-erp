package audit

import "fmt"

// Text returns a localised user-facing string. lang is "en" or "ar".
// Format args fill %s / %d placeholders in the catalogue entry.
func Text(lang, key string, args ...any) string {
	table, ok := messages[lang]
	if !ok {
		table = messages["en"]
	}
	s, ok := table[key]
	if !ok {
		s = messages["en"][key]
	}
	if s == "" {
		return key
	}
	if len(args) == 0 {
		return s
	}
	return fmt.Sprintf(s, args...)
}

// messages holds every string a person sees. Workers and logs stay in code.
var messages = map[string]map[string]string{
	"en": {
		"error.auth_required":       "authentication required",
		"error.forbidden":           "you do not have permission to do this",
		"error.validation":          "the request is not valid",
		"error.not_found":           "not found",
		"error.internal":            "something went wrong",
		"verify.hash_mismatch":      "recomputed hash does not match the stored hash",
		"verify.link_mismatch":      "prev_hash does not match the previous row",
		"verify.seq_gap":            "chain sequence is not contiguous",
		"verify.anchor_mismatch":    "recomputed head does not match the anchored head",
		"verify.anchor_stale":       "the off-site anchor has not been written for two hours",
		"email.anchor.subject":      "Smart ERP daily chain anchor",
		"email.anchor.body":         "Company %s chain sequence %d head %s at %s.",
		"restore.approval_required": "a Stakeholder must approve this restore before it can serve writes",
		"restore.refused":           "restore refused: %s",
		"status.never":              "no run recorded",
		"status.wal_lag":            "wal archive lag %s; recovery point %s",
		"config.missing":            "missing required environment: %s",
	},
	"ar": {
		"error.auth_required":       "يلزم تسجيل الدخول",
		"error.forbidden":           "ليست لديك صلاحية لهذا الإجراء",
		"error.validation":          "الطلب غير صالح",
		"error.not_found":           "غير موجود",
		"error.internal":            "حدث خطأ",
		"verify.hash_mismatch":      "التجزئة المعاد حسابها لا تطابق القيمة المخزنة",
		"verify.link_mismatch":      "prev_hash لا يطابق الصف السابق",
		"verify.seq_gap":            "تسلسل السلسلة غير متصل",
		"verify.anchor_mismatch":    "رأس السلسلة المعاد حسابه لا يطابق المرساة",
		"verify.anchor_stale":       "لم تُكتب مرساة خارج الموقع منذ ساعتين",
		"email.anchor.subject":      "مرساة سلسلة Smart ERP اليومية",
		"email.anchor.body":         "الشركة %s تسلسل السلسلة %d الرأس %s في %s.",
		"restore.approval_required": "يجب أن يوافق صاحب المصلحة قبل أن يقبل الاسترجاع عمليات الكتابة",
		"restore.refused":           "رُفض الاسترجاع: %s",
		"status.never":              "لا يوجد تشغيل مسجّل",
		"status.wal_lag":            "تأخر أرشفة WAL %s؛ نقطة الاسترجاع %s",
		"config.missing":            "متغيرات بيئة مطلوبة ناقصة: %s",
	},
}
