package identity

import "strings"

// User-facing copy lives here and nowhere else in this package (en and ar).
const (
	msgAuthFailed     = "auth.failed"
	msgRateLimited    = "rate.limited"
	msgValidation     = "validation.body"
	msgNotFound       = "not_found"
	msgTokenExpired   = "token.expired"
	msgTokenRevoked   = "token.revoked"
	msgDeviceMismatch = "device.mismatch"
	msgStepUpRequired = "step_up.required"
	msgStepUpMethod   = "step_up.method"
	msgForbidden      = "forbidden"
	msgInternal       = "internal"
	msgUnavailable    = "unavailable"
	msgMissingConfig  = "missing_config"
)

var catalog = map[string]map[string]string{
	"en": {
		msgAuthFailed:     "Authentication failed",
		msgRateLimited:    "Too many requests",
		msgValidation:     "Request is invalid",
		msgNotFound:       "Not found",
		msgTokenExpired:   "Access token has expired",
		msgTokenRevoked:   "Access token has been revoked",
		msgDeviceMismatch: "Device key does not match the enrolled device",
		msgStepUpRequired: "Step-up authentication is required",
		msgStepUpMethod:   "Step-up method is not supported",
		msgForbidden:      "You cannot perform this action",
		msgInternal:       "Internal error",
		msgUnavailable:    "Authentication service is unavailable",
		msgMissingConfig:  "Authentication is not configured",
	},
	"ar": {
		msgAuthFailed:     "فشلت المصادقة",
		msgRateLimited:    "عدد الطلبات كبير جدا",
		msgValidation:     "الطلب غير صالح",
		msgNotFound:       "غير موجود",
		msgTokenExpired:   "انتهت صلاحية رمز الدخول",
		msgTokenRevoked:   "تم إبطال رمز الدخول",
		msgDeviceMismatch: "مفتاح الجهاز لا يطابق الجهاز المسجل",
		msgStepUpRequired: "يلزم تحقق إضافي",
		msgStepUpMethod:   "طريقة التحقق الإضافي غير مدعومة",
		msgForbidden:      "لا يمكنك تنفيذ هذا الإجراء",
		msgInternal:       "حدث خطأ داخلي",
		msgUnavailable:    "خدمة المصادقة غير متاحة",
		msgMissingConfig:  "المصادقة غير مهيأة",
	},
}

func langOf(header string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(header)), "ar") {
		return "ar"
	}
	return "en"
}

func translate(lang, key string) string {
	if table, ok := catalog[lang]; ok {
		if msg, ok := table[key]; ok {
			return msg
		}
	}
	return catalog["en"][key]
}
