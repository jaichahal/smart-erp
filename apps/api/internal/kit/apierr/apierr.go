// Package apierr is the single error envelope for every failure path (04 "Envelopes").
package apierr

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

// Code is one of the fixed codes in 04-api-contracts.md. Add here first, then in the OpenAPI enum.
type Code string

// Codes, one per failure class; the HTTP status lives in statusByCode.
const (
	AuthRequired     Code = "AUTH_REQUIRED"
	TokenExpired     Code = "TOKEN_EXPIRED"
	TokenRevoked     Code = "TOKEN_REVOKED"
	DeviceMismatch   Code = "DEVICE_MISMATCH"
	StepUpRequired   Code = "STEP_UP_REQUIRED"
	PermissionDenied Code = "PERMISSION_DENIED"
	ValidationError  Code = "VALIDATION_ERROR"
	MissingConfig    Code = "MISSING_CONFIG"
	NotFound         Code = "NOT_FOUND"
	Conflict         Code = "CONFLICT"
	AlreadyDecided   Code = "ALREADY_DECIDED"
	Immutable        Code = "IMMUTABLE"
	PeriodClosed     Code = "PERIOD_CLOSED"
	SoDViolation     Code = "SOD_VIOLATION"
	CreditHold       Code = "CREDIT_HOLD"
	PriceFloorHold   Code = "PRICE_FLOOR_HOLD"
	NegativeStock    Code = "NEGATIVE_STOCK"
	RateLimited      Code = "RATE_LIMITED"
	Internal         Code = "INTERNAL_ERROR"
)

// All is the closed set; tests assert the OpenAPI enum equals this.
var All = []Code{
	AuthRequired, TokenExpired, TokenRevoked, DeviceMismatch, StepUpRequired, PermissionDenied,
	ValidationError, MissingConfig, NotFound, Conflict, AlreadyDecided, Immutable, PeriodClosed,
	SoDViolation, CreditHold, PriceFloorHold, NegativeStock, RateLimited, Internal,
}

var statusByCode = map[Code]int{
	AuthRequired: 401, TokenExpired: 401, TokenRevoked: 401, DeviceMismatch: 401, StepUpRequired: 403,
	PermissionDenied: 403, ValidationError: 400, MissingConfig: 400, NotFound: 404, Conflict: 409,
	AlreadyDecided: 409, Immutable: 409, PeriodClosed: 409, SoDViolation: 403, CreditHold: 409,
	PriceFloorHold: 409, NegativeStock: 409, RateLimited: 429, Internal: 500,
}

// Error is a typed API error. Details are safe to show to the caller.
type Error struct {
	Code    Code           `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
	cause   error
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }
func (e *Error) Unwrap() error { return e.cause }

// Status is the HTTP status for the code.
func (e *Error) Status() int {
	if s, ok := statusByCode[e.Code]; ok {
		return s
	}
	return 500
}

// New builds an error with the code and a caller-safe message.
func New(code Code, msg string) *Error { return &Error{Code: code, Message: msg} }

// Wrap attaches an internal cause that is logged but never returned to the caller.
func Wrap(code Code, msg string, cause error) *Error {
	return &Error{Code: code, Message: msg, cause: cause}
}

// WithDetails adds structured, caller-safe details.
func (e *Error) WithDetails(d map[string]any) *Error { e.Details = d; return e }

type envelope struct {
	Error body `json:"error"`
}
type body struct {
	Code      Code           `json:"code"`
	Message   string         `json:"message"`
	Details   map[string]any `json:"details"`
	RequestID string         `json:"request_id"`
}

// Write serialises any error into the envelope with the right status. Unknown
// errors become INTERNAL_ERROR with a generic message; the cause is logged.
func Write(w http.ResponseWriter, r *http.Request, err error) {
	var ae *Error
	if !errors.As(err, &ae) {
		ae = Wrap(Internal, "internal error", err)
	}
	rid := RequestID(r.Context())
	if ae.Code == Internal || ae.cause != nil {
		slog.ErrorContext(r.Context(), "request failed", "code", ae.Code, "request_id", rid, "err", err)
	}
	details := ae.Details
	if details == nil {
		details = map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(ae.Status())
	if err := json.NewEncoder(w).Encode(envelope{Error: body{Code: ae.Code, Message: ae.Message, Details: details, RequestID: rid}}); err != nil {
		slog.WarnContext(r.Context(), "write error envelope", "err", err)
	}
}
