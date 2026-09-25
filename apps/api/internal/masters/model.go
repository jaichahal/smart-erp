package masters

const (
	docCustomer   = "customer_master"
	docVendor     = "vendor_onboarding"
	docVendorEdit = "vendor_master"
	docBank       = "vendor_bank_change"
	docVendorSKU  = "vendor_sku"
	docBlacklist  = "vendor_blacklist"
	docSKU        = "sku_master"
	docBOM        = "bom"
	docPriceList  = "price_list"
	docAgreement  = "price_agreement"
	docTerms      = "payment_terms"
	moneyScale    = 4
	qtyScale      = 6
)

func executiveDoc(docType string) bool {
	switch docType {
	case docVendor, docVendorSKU, docBlacklist:
		return true
	default:
		return false
	}
}
