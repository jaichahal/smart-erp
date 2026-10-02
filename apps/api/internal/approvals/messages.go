package approvals

import (
	"context"
	"fmt"
	"strings"
)

type langKey struct{}

// WithLang stores the message language (en or ar) on the context.
func WithLang(ctx context.Context, lang string) context.Context {
	lang = strings.ToLower(strings.TrimSpace(lang))
	switch {
	case strings.HasPrefix(lang, "ar"):
		lang = "ar"
	default:
		lang = "en"
	}
	return context.WithValue(ctx, langKey{}, lang)
}

func language(ctx context.Context) string {
	if v, ok := ctx.Value(langKey{}).(string); ok && v != "" {
		return v
	}
	return "en"
}

// messages holds every user-facing string. English and Arabic stay in lockstep.
var messages = map[string]map[string]string{
	"en": {
		"auth.required":              "Authentication required",
		"auth.not_eligible":          "You cannot decide this request",
		"auth.not_in_directory":      "Actor is not in the company directory",
		"reason.required":            "A non-empty reason is required",
		"reason.too_long":            "Reason is too long",
		"sod.self_approval":          "The submitter cannot approve their own document",
		"sod.accountant_self_assign": "An accountant cannot assign themselves as approver",
		"sod.accounts_independence":  "Someone in Accounts cannot give the first approval when the initiator is in Accounts",
		"stepup.required":            "Step-up authentication is required",
		"delegate.final_gate":        "The final gate cannot be delegated",
		"delegate.until_past":        "Delegation must end in the future",
		"delegate.expired":           "This delegation has expired",
		"delegate.not_allowed":       "This stage cannot be delegated",
		"token.expired":              "The posting token has expired",
		"token.used":                 "The posting token was already used",
		"token.unknown":              "The posting token is not valid",
		"token.hash_mismatch":        "The posted content does not match the approved content hash",
		"version.missing":            "state_version is required",
		"config.missing_matrix":      "No approval matrix is configured for this document type",
		"validation.amount":          "Amount must be a decimal string",
		"validation.currency":        "Currency must be a three-letter code",
		"validation.doc_type":        "Document type is not valid",
		"validation.json":            "Invalid JSON body",
		"validation.slot":            "Approver slot is not valid",
		"inbox.state":                "Inbox state must be needs_me, waiting_on_others or fyi",
		"request.not_found":          "Approval request not found",
		"fraud.variance":             "Amount differs by more than %s%% from the comparison amount",
		"fraud.round":                "Round amount at or above %s",
		"fraud.rejections":           "Repeated rejections in this chain: %d",
		"fraud.corrections":          "Corrections raised by this user this month: %d",
		"fraud.original_approver":    "You approved the original document",
	},
	"ar": {
		"auth.required":              "يلزم تسجيل الدخول",
		"auth.not_eligible":          "لا يمكنك اتخاذ قرار بشأن هذا الطلب",
		"auth.not_in_directory":      "المستخدم غير موجود في دليل الشركة",
		"reason.required":            "يلزم سبب غير فارغ",
		"reason.too_long":            "السبب أطول من الحد المسموح",
		"sod.self_approval":          "لا يمكن لمقدم الطلب اعتماد مستنده",
		"sod.accountant_self_assign": "لا يمكن للمحاسب تعيين نفسه معتمداً",
		"sod.accounts_independence":  "لا يمكن لأحد من الحسابات منح الاعتماد الأول إذا كان المقدم من الحسابات",
		"stepup.required":            "يلزم تأكيد إضافي للخطوة الحساسة",
		"delegate.final_gate":        "لا يمكن تفويض البوابة النهائية",
		"delegate.until_past":        "يجب أن ينتهي التفويض في وقت لاحق",
		"delegate.expired":           "انتهت صلاحية هذا التفويض",
		"delegate.not_allowed":       "لا يمكن تفويض هذه المرحلة",
		"token.expired":              "انتهت صلاحية رمز الترحيل",
		"token.used":                 "استُخدم رمز الترحيل من قبل",
		"token.unknown":              "رمز الترحيل غير صالح",
		"token.hash_mismatch":        "المحتوى المرحّل لا يطابق البصمة المعتمدة",
		"version.missing":            "يلزم رقم إصدار الحالة",
		"config.missing_matrix":      "لا توجد مصفوفة اعتماد لهذا النوع من المستندات",
		"validation.amount":          "يجب أن يكون المبلغ نصاً عشرياً",
		"validation.currency":        "يجب أن يكون رمز العملة من ثلاثة أحرف",
		"validation.doc_type":        "نوع المستند غير صالح",
		"validation.json":            "جسم الطلب ليس JSON صالحاً",
		"validation.slot":            "خانة المعتمد غير صالحة",
		"inbox.state":                "حالة الصندوق يجب أن تكون needs_me أو waiting_on_others أو fyi",
		"request.not_found":          "طلب الاعتماد غير موجود",
		"fraud.variance":             "يختلف المبلغ بأكثر من %s%% عن مبلغ المقارنة",
		"fraud.round":                "مبلغ مدور عند أو فوق %s",
		"fraud.rejections":           "رفض متكرر في هذه السلسلة: %d",
		"fraud.corrections":          "تصحيحات رفعها هذا المستخدم هذا الشهر: %d",
		"fraud.original_approver":    "أنت من اعتمد المستند الأصلي",
	},
}

func t(ctx context.Context, key string, args ...any) string {
	lang := language(ctx)
	format := messages[lang][key]
	if format == "" {
		format = messages["en"][key]
	}
	if format == "" {
		return key
	}
	if len(args) == 0 {
		return format
	}
	return fmt.Sprintf(format, args...)
}
