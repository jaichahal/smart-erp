// Package ifmatch implements optimistic concurrency on state_version (04 "Concurrency").
package ifmatch

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
)

// Header is the request header carrying the expected state_version.
const Header = "If-Match"

// Parse returns the expected version from If-Match. required=false lets a caller
// treat an absent header as "no check" (only for endpoints 04 marks optional).
func Parse(r *http.Request, required bool) (int64, bool, error) {
	raw := strings.Trim(strings.TrimSpace(r.Header.Get(Header)), `"`)
	if raw == "" {
		if required {
			return 0, false, apierr.New(apierr.ValidationError, "If-Match header with state_version is required")
		}
		return 0, false, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 0 {
		return 0, false, apierr.New(apierr.ValidationError, "If-Match must be a non-negative integer state_version")
	}
	return v, true, nil
}

// Check compares expected and current and returns the 409 CONFLICT error with the
// current state in details when they differ. current may be any JSON-able value
// the client can use to retry.
func Check(expected, actual int64, current any) error {
	if expected == actual {
		return nil
	}
	return apierr.New(apierr.Conflict, "state_version mismatch").WithDetails(map[string]any{
		"expected_state_version": expected,
		"current_state_version":  actual,
		"current":                current,
	})
}
