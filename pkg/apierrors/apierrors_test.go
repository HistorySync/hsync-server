package apierrors

import (
	"net/http"
	"testing"
)

func TestLookupKnownCode(t *testing.T) {
	e := Lookup(CodeEmailTaken)
	if e.Code != CodeEmailTaken {
		t.Fatalf("Code = %q, want %q", e.Code, CodeEmailTaken)
	}
	if e.HTTPStatus != http.StatusConflict {
		t.Fatalf("HTTPStatus = %d, want %d", e.HTTPStatus, http.StatusConflict)
	}
}

func TestLookupUnknownCode(t *testing.T) {
	e := Lookup("NO_SUCH_CODE")
	if e.Code != CodeInternalError {
		t.Fatalf("Code = %q, want %q", e.Code, CodeInternalError)
	}
	if e.HTTPStatus != http.StatusInternalServerError {
		t.Fatalf("HTTPStatus = %d, want %d", e.HTTPStatus, http.StatusInternalServerError)
	}
}

func TestAllReturnsSorted(t *testing.T) {
	entries := All()
	if len(entries) != len(catalog) {
		t.Fatalf("All() len = %d, want %d", len(entries), len(catalog))
	}
	for i := 1; i < len(entries); i++ {
		if entries[i-1].Code >= entries[i].Code {
			t.Fatalf("All() not sorted: [%d]=%q >= [%d]=%q",
				i-1, entries[i-1].Code, i, entries[i].Code)
		}
	}
}

func TestNewWithDetail(t *testing.T) {
	e := New(CodeEmailTaken, "email foo@bar.com is already registered")
	if e.Code != CodeEmailTaken {
		t.Fatalf("Code = %q, want %q", e.Code, CodeEmailTaken)
	}
	if e.HTTPStatus != http.StatusConflict {
		t.Fatalf("HTTPStatus = %d, want %d", e.HTTPStatus, http.StatusConflict)
	}
	if e.Message != "email foo@bar.com is already registered" {
		t.Fatalf("Message = %q, want detail text", e.Message)
	}
	if e.Detail != "email foo@bar.com is already registered" {
		t.Fatalf("Detail not preserved")
	}
}

func TestNewEmptyDetailUsesDefault(t *testing.T) {
	e := New(CodeNotFound, "")
	if e.Message != catalog[CodeNotFound].Message {
		t.Fatalf("Message = %q, want default %q", e.Message, catalog[CodeNotFound].Message)
	}
}

func TestNewBadRequest(t *testing.T) {
	e := NewBadRequest("missing 'bundle' field")
	if e.Code != CodeBadRequest {
		t.Fatalf("Code = %q, want %q", e.Code, CodeBadRequest)
	}
	if e.HTTPStatus != http.StatusBadRequest {
		t.Fatalf("HTTPStatus = %d, want %d", e.HTTPStatus, http.StatusBadRequest)
	}
}

func TestNewInternal(t *testing.T) {
	e := NewInternal("db connection lost")
	if e.Code != CodeInternalError {
		t.Fatalf("Code = %q, want %q", e.Code, CodeInternalError)
	}
	if e.HTTPStatus != http.StatusInternalServerError {
		t.Fatalf("HTTPStatus = %d, want %d", e.HTTPStatus, http.StatusInternalServerError)
	}
}

func TestNewf(t *testing.T) {
	e := Newf(CodeNotFound, "bundle %s not found", "abc123")
	if e.Message != "bundle abc123 not found" {
		t.Fatalf("Message = %q", e.Message)
	}
	if e.Code != CodeNotFound {
		t.Fatalf("Code = %q, want %q", e.Code, CodeNotFound)
	}
}

func TestErrorInterface(t *testing.T) {
	e := New(CodeConflict, "it conflicts")
	if e.Error() != "it conflicts" {
		t.Fatalf("Error() = %q, want %q", e.Error(), "it conflicts")
	}
}

func TestEveryCodeInCatalog(t *testing.T) {
	// Enforce that every Code constant has a catalog entry.
	codes := []Code{
		CodeBadRequest, CodeInternalError, CodeNotImplemented, CodeNotFound,
		CodeConflict, CodeEmailTaken, CodeInvalidCredentials,
		CodeInvalidRefreshToken, CodeInvalidResetToken, CodeInvalidVerificationToken,
		CodeQuotaExceeded, CodeReservationDenied,
		CodeDeviceNotRegistered, CodeDeviceRevoked,
		CodeBillingDisabled,
		CodeInvalidUserID, CodeUserNotFound,
		CodeInvalidJSON, CodeMissingKey, CodeOptionsDisabled,
	}
	for _, c := range codes {
		if _, ok := catalog[c]; !ok {
			t.Fatalf("Code %q missing from catalog", c)
		}
	}
}

func TestAllHTTPStatusesAreValid(t *testing.T) {
	for _, e := range catalog {
		if e.HTTPStatus < 100 || e.HTTPStatus >= 600 {
			t.Fatalf("Code %q has invalid HTTP status %d", e.Code, e.HTTPStatus)
		}
	}
}
