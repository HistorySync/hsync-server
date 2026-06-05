// Package apierrors provides a centralized catalog of API error codes with
// HTTP status codes and human-readable English messages. Handler code references
// these codes instead of inline strings, keeping error responses consistent and
// enabling future i18n via the Accept-Language header.
//
// Usage in handlers:
//
//	// Domain-specific error (code from catalog, service message as detail):
//	return apierrors.New(apierrors.CodeEmailTaken, err.Error())
//
//	// Generic bad request (code = BAD_REQUEST):
//	return apierrors.NewBadRequest("missing 'bundle' file field")
//
//	// Generic internal error (code = INTERNAL_ERROR):
//	return apierrors.NewInternal(err.Error())
//
// The ErrorHandler in pkg/handler checks for *Error and sets the HTTP status
// and response body accordingly. Unknown error types fall through to 500 /
// INTERNAL_ERROR.
package apierrors

import (
	"fmt"
	"net/http"
	"sort"
)

// Code is a machine-readable error code returned in API responses under the
// "error.code" key. Use the constants defined below rather than a string
// literal.
type Code string

// ── Error Code Constants ──────────────────────────────────────────
//
// Sorted alphabetically within each group. When adding a code also add its
// catalog entry below.

const (
	// Generic
	CodeBadRequest     Code = "BAD_REQUEST"
	CodeInternalError  Code = "INTERNAL_ERROR"
	CodeNotImplemented Code = "NOT_IMPLEMENTED"
	CodeNotFound       Code = "NOT_FOUND"
	CodeRateLimited    Code = "RATE_LIMITED"

	// Auth
	CodeConflict                  Code = "CONFLICT"
	CodeEmailTaken                Code = "EMAIL_TAKEN"
	CodeInvalidCredentials        Code = "INVALID_CREDENTIALS"
	CodeInvalidRefreshToken       Code = "INVALID_REFRESH_TOKEN"
	CodeInvalidResetToken         Code = "INVALID_RESET_TOKEN"
	CodeInvalidVerificationToken  Code = "INVALID_VERIFICATION_TOKEN"
	CodeStepUpExpired             Code = "STEP_UP_EXPIRED"
	CodeStepUpInvalid             Code = "STEP_UP_INVALID"
	CodeStepUpRequired            Code = "STEP_UP_REQUIRED"
	CodeTwoFactorAlreadyEnabled   Code = "TWO_FACTOR_ALREADY_ENABLED"
	CodeTwoFactorChallengeInvalid Code = "TWO_FACTOR_CHALLENGE_INVALID"
	CodeTwoFactorInvalidCode      Code = "TWO_FACTOR_INVALID_CODE"
	CodeTwoFactorLocked           Code = "TWO_FACTOR_LOCKED"
	CodeTwoFactorNotEnabled       Code = "TWO_FACTOR_NOT_ENABLED"
	CodeTwoFactorRequired         Code = "TWO_FACTOR_REQUIRED"
	CodeTurnstileFailed           Code = "TURNSTILE_FAILED"
	CodeTurnstileRequired         Code = "TURNSTILE_REQUIRED"
	CodeTurnstileUnavailable      Code = "TURNSTILE_UNAVAILABLE"

	// Quota / reservation
	CodeQuotaExceeded     Code = "QUOTA_EXCEEDED"
	CodeReservationDenied Code = "RESERVATION_DENIED"

	// Device
	CodeDeviceNotRegistered Code = "DEVICE_NOT_REGISTERED"
	CodeDeviceRevoked       Code = "DEVICE_REVOKED"

	// Billing
	CodeBillingDisabled Code = "BILLING_DISABLED"

	// User admin
	CodeInvalidUserID Code = "INVALID_USER_ID"
	CodeUserNotFound  Code = "USER_NOT_FOUND"

	// Dynamic options
	CodeInvalidJSON     Code = "INVALID_JSON"
	CodeMissingKey      Code = "MISSING_KEY"
	CodeOptionsDisabled Code = "OPTIONS_DISABLED"
)

// ── Entry: one row in the error catalog ───────────────────────────

// Entry describes a single error code as it appears in API responses and in
// the GET /admin/error-codes documentation endpoint.
type Entry struct {
	Code       Code   `json:"code"`
	HTTPStatus int    `json:"http_status"`
	Message    string `json:"message"` // English default message
}

// ── Catalog ───────────────────────────────────────────────────────
//
// Every Code constant MUST have an entry here. The Message field is the
// English fallback; future i18n support will extend this with per-locale
// translations keyed by BCP-47 language tag.

var catalog = map[Code]Entry{
	CodeBadRequest:                {CodeBadRequest, http.StatusBadRequest, "bad request"},
	CodeInternalError:             {CodeInternalError, http.StatusInternalServerError, "internal server error"},
	CodeNotImplemented:            {CodeNotImplemented, http.StatusNotImplemented, "not implemented"},
	CodeNotFound:                  {CodeNotFound, http.StatusNotFound, "not found"},
	CodeRateLimited:               {CodeRateLimited, http.StatusTooManyRequests, "rate limit exceeded, retry later"},
	CodeConflict:                  {CodeConflict, http.StatusConflict, "conflict"},
	CodeEmailTaken:                {CodeEmailTaken, http.StatusConflict, "email already registered"},
	CodeInvalidCredentials:        {CodeInvalidCredentials, http.StatusUnauthorized, "invalid email or password"},
	CodeInvalidRefreshToken:       {CodeInvalidRefreshToken, http.StatusUnauthorized, "invalid or expired refresh token"},
	CodeInvalidResetToken:         {CodeInvalidResetToken, http.StatusUnauthorized, "invalid or expired reset token"},
	CodeInvalidVerificationToken:  {CodeInvalidVerificationToken, http.StatusUnauthorized, "invalid or expired verification token"},
	CodeStepUpExpired:             {CodeStepUpExpired, http.StatusForbidden, "step-up verification token is expired"},
	CodeStepUpInvalid:             {CodeStepUpInvalid, http.StatusForbidden, "invalid step-up verification token"},
	CodeStepUpRequired:            {CodeStepUpRequired, http.StatusForbidden, "step-up verification is required"},
	CodeTwoFactorAlreadyEnabled:   {CodeTwoFactorAlreadyEnabled, http.StatusConflict, "two-factor authentication is already enabled"},
	CodeTwoFactorChallengeInvalid: {CodeTwoFactorChallengeInvalid, http.StatusUnauthorized, "invalid or expired two-factor challenge"},
	CodeTwoFactorInvalidCode:      {CodeTwoFactorInvalidCode, http.StatusUnauthorized, "invalid two-factor authentication code"},
	CodeTwoFactorLocked:           {CodeTwoFactorLocked, http.StatusTooManyRequests, "two-factor authentication is temporarily locked"},
	CodeTwoFactorNotEnabled:       {CodeTwoFactorNotEnabled, http.StatusBadRequest, "two-factor authentication is not enabled"},
	CodeTwoFactorRequired:         {CodeTwoFactorRequired, http.StatusUnauthorized, "two-factor authentication is required"},
	CodeTurnstileFailed:           {CodeTurnstileFailed, http.StatusForbidden, "turnstile verification failed"},
	CodeTurnstileRequired:         {CodeTurnstileRequired, http.StatusBadRequest, "turnstile token is required"},
	CodeTurnstileUnavailable:      {CodeTurnstileUnavailable, http.StatusServiceUnavailable, "turnstile verification is unavailable"},
	CodeQuotaExceeded:             {CodeQuotaExceeded, 507, "storage quota exceeded"},
	CodeReservationDenied:         {CodeReservationDenied, http.StatusForbidden, "reservation denied"},
	CodeDeviceNotRegistered:       {CodeDeviceNotRegistered, http.StatusBadRequest, "device not registered"},
	CodeDeviceRevoked:             {CodeDeviceRevoked, http.StatusForbidden, "device has been revoked"},
	CodeBillingDisabled:           {CodeBillingDisabled, http.StatusServiceUnavailable, "billing is not available"},
	CodeInvalidUserID:             {CodeInvalidUserID, http.StatusBadRequest, "invalid user id"},
	CodeUserNotFound:              {CodeUserNotFound, http.StatusNotFound, "user not found"},
	CodeInvalidJSON:               {CodeInvalidJSON, http.StatusBadRequest, "invalid JSON body"},
	CodeMissingKey:                {CodeMissingKey, http.StatusBadRequest, "option key is required"},
	CodeOptionsDisabled:           {CodeOptionsDisabled, http.StatusNotImplemented, "dynamic options are disabled"},
}

// Lookup returns the catalog entry for c. Unknown codes fall back to the
// INTERNAL_ERROR entry.
func Lookup(c Code) Entry {
	if e, ok := catalog[c]; ok {
		return e
	}
	return catalog[CodeInternalError]
}

// All returns a sorted slice of every registered error code. Used by the
// GET /admin/error-codes endpoint.
func All() []Entry {
	entries := make([]Entry, 0, len(catalog))
	for _, e := range catalog {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Code < entries[j].Code
	})
	return entries
}

// ── Error type ────────────────────────────────────────────────────

// Error is an API error that carries a machine-readable Code and a
// human-readable Message. It implements the error interface so handlers
// can return it directly to Fiber's error handler chain.
type Error struct {
	Code       Code   `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"-"`
	Detail     string `json:"-"` // original detail for logging, not sent to clients
}

// Error returns the message so *Error implements error.
func (e *Error) Error() string { return e.Message }

// ── Constructors ──────────────────────────────────────────────────

// New returns an *Error for the given code. When detail is non-empty it is
// used as the Message (preserving the service-layer error text); otherwise
// the catalog default message is used.
func New(code Code, detail string) *Error {
	e := Lookup(code)
	msg := e.Message
	if detail != "" {
		msg = detail
	}
	return &Error{Code: code, Message: msg, HTTPStatus: e.HTTPStatus, Detail: detail}
}

// Newf is like New but formats the detail string.
func Newf(code Code, format string, args ...any) *Error {
	return New(code, fmt.Sprintf(format, args...))
}

// NewBadRequest returns a BAD_REQUEST error with the given detail.
func NewBadRequest(detail string) *Error {
	return New(CodeBadRequest, detail)
}

// NewInternal returns an INTERNAL_ERROR with the given detail.
func NewInternal(detail string) *Error {
	return New(CodeInternalError, detail)
}
