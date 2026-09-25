package purchase

var en = map[string]string{
	"title.required":     "Only a CFO or a Partner can decide this",
	"title.bootstrap":    "Only a current CFO or Partner can change that list",
	"not.delegable":      "This decision is not delegable",
	"prepare.only":       "The accountant may prepare this file and cannot approve it",
	"sku.required":       "This raw material requires a vendor approved for that SKU",
	"vendor.required":    "The vendor is not approved",
	"vendor.blacklisted": "A blacklisted vendor cannot be used on this document",
	"vendor.skip":        "A vendor insert that skips the approval request is refused",
	"token.invalid":      "The posting token does not approve this vendor",
	"payment.held":       "This supplier invoice cannot be paid until a CFO or Partner releases that payment",
	"payment.preparer":   "The user who prepared the vendor cannot pay that vendor",
	"stepup.required":    "Step-up is required",
	"duplicate.invoice":  "This supplier invoice matches another invoice and stays blocked until confirmed",
	"match.blocked":      "The supplier invoice is outside three-way match tolerance and does not post",
	"quote.unapproved":   "The quote is not approved",
	"not.found":          "Not found",
	"validation":         "The purchase document is not valid",
}

func msg(key string) string {
	if s, ok := en[key]; ok {
		return s
	}
	return key
}
