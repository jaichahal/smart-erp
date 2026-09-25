package journeys

import "strings"

// text returns the localised message for key. Accept-Language is en or ar;
// anything else is English. A missing key returns the key so a gap is visible
// in tests rather than a hard-coded sentence at the call site.
func text(lang, key string) string {
	l := "en"
	if strings.HasPrefix(strings.ToLower(lang), "ar") {
		l = "ar"
	}
	if m, ok := messages[l][key]; ok {
		return m
	}
	if m, ok := messages["en"][key]; ok {
		return m
	}
	return key
}

var messages = map[string]map[string]string{
	"en": {
		"journey.auth_required":                       "Sign in to continue this journey.",
		"journey.permission_denied":                   "You do not have permission to complete this step.",
		"journey.pending":                             "Waiting on a decision. Nothing has been approved.",
		"journey.rejected":                            "The awaited decision was rejected. This journey has stopped.",
		"journey.stopped":                             "This journey has stopped and cannot continue.",
		"journey.validation":                          "The step input is not valid.",
		"journey.field_required":                      "A required field is missing.",
		"journey.trial_balance":                       "The opening trial balance does not net to zero. The company stays locked.",
		"journey.step_mismatch":                       "That is not the current step.",
		"journey.step_completed":                      "Step completed.",
		"journey.not_found":                           "Journey not found.",
		"journey.persona_denied":                      "You do not hold that persona.",
		"journey.already_finished":                    "This journey has already finished.",
		"journey.go_live.title":                       "Go-Live",
		"journey.go_live.step.chart_of_accounts":      "Chart of accounts and opening balances",
		"journey.go_live.step.open_customer_invoices": "Open customer invoices",
		"journey.go_live.step.open_supplier_invoices": "Open supplier invoices",
		"journey.go_live.step.opening_stock":          "Opening stock per SKU with cost",
		"journey.go_live.step.opening_assets":         "Opening assets",
		"journey.go_live.step.bank_balances":          "Bank balances",
		"journey.go_live.step.trial_balance":          "Opening trial balance nets to zero",
		"journey.go_live.step.stakeholder_approval":   "Stakeholder approval",
		"journey.go_live.step.unlock_company":         "Unlock the company",
		"journey.probe.permission_title":              "Permission probe",
		"journey.probe.permission_step":               "Guarded step",
		"journey.probe.await_title":                   "Await probe",
		"journey.probe.note":                          "Note",
		"journey.probe.wait":                          "Wait for a decision",
		"journey.probe.finish":                        "Finish",
		"journey.probe.resume_title":                  "Resume probe",
		"journey.probe.one":                           "First step",
		"journey.probe.two":                           "Second step",
		"journey.probe.stakeholder_title":             "Stakeholder home",
		"journey.probe.stakeholder_step":              "Brief",
	},
	"ar": {
		"journey.auth_required":                       "سجّل الدخول لمتابعة هذه الرحلة.",
		"journey.permission_denied":                   "ليست لديك صلاحية إكمال هذه الخطوة.",
		"journey.pending":                             "في انتظار قرار. لم تتم الموافقة على شيء.",
		"journey.rejected":                            "رُفض القرار المنتظر. توقفت هذه الرحلة.",
		"journey.stopped":                             "توقفت هذه الرحلة ولا يمكن متابعتها.",
		"journey.validation":                          "إدخال الخطوة غير صالح.",
		"journey.field_required":                      "حقل مطلوب مفقود.",
		"journey.trial_balance":                       "ميزان المراجعة الافتتاحي لا يساوي صفرًا. تبقى الشركة مقفلة.",
		"journey.step_mismatch":                       "هذه ليست الخطوة الحالية.",
		"journey.step_completed":                      "اكتملت الخطوة.",
		"journey.not_found":                           "الرحلة غير موجودة.",
		"journey.persona_denied":                      "لا تملك هذه الشخصية.",
		"journey.already_finished":                    "انتهت هذه الرحلة من قبل.",
		"journey.go_live.title":                       "بدء التشغيل",
		"journey.go_live.step.chart_of_accounts":      "دليل الحسابات والأرصدة الافتتاحية",
		"journey.go_live.step.open_customer_invoices": "فواتير العملاء المفتوحة",
		"journey.go_live.step.open_supplier_invoices": "فواتير الموردين المفتوحة",
		"journey.go_live.step.opening_stock":          "المخزون الافتتاحي لكل صنف مع التكلفة",
		"journey.go_live.step.opening_assets":         "الأصول الافتتاحية",
		"journey.go_live.step.bank_balances":          "أرصدة البنك",
		"journey.go_live.step.trial_balance":          "ميزان المراجعة الافتتاحي يساوي صفرًا",
		"journey.go_live.step.stakeholder_approval":   "موافقة صاحب المصلحة",
		"journey.go_live.step.unlock_company":         "فتح الشركة",
		"journey.probe.permission_title":              "اختبار الصلاحية",
		"journey.probe.permission_step":               "خطوة محمية",
		"journey.probe.await_title":                   "اختبار الانتظار",
		"journey.probe.note":                          "ملاحظة",
		"journey.probe.wait":                          "انتظار قرار",
		"journey.probe.finish":                        "إنهاء",
		"journey.probe.resume_title":                  "اختبار الاستئناف",
		"journey.probe.one":                           "الخطوة الأولى",
		"journey.probe.two":                           "الخطوة الثانية",
		"journey.probe.stakeholder_title":             "الرئيسية لصاحب المصلحة",
		"journey.probe.stakeholder_step":              "الموجز",
	},
}
