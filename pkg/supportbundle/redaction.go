package supportbundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"time"
)

const redactedValue = "<redacted>"

var emailPattern = regexp.MustCompile(`(?i)[a-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+`)

// Redact applies the support-bundle redaction policy to any JSON-like value.
// It is intentionally conservative: sensitive field names are replaced, URLs
// have credentials/query secrets removed, and email-looking strings are masked.
func Redact(value any) any {
	return redactValue("", value)
}

func redactValue(key string, value any) any {
	if value == nil {
		return nil
	}
	if sensitiveFieldName(key) {
		return redactedForKey(key, value)
	}
	switch typed := value.(type) {
	case string:
		return redactString(key, typed)
	case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return value
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, v := range typed {
			out[k] = redactValue(k, v)
		}
		return out
	case map[string]string:
		out := make(map[string]any, len(typed))
		for k, v := range typed {
			out[k] = redactValue(k, v)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, redactValue(key, item))
		}
		return out
	case json.RawMessage:
		return redactRawJSON(key, typed)
	case time.Time:
		return typed.UTC()
	default:
		return redactReflect(key, value)
	}
}

func redactReflect(key string, value any) any {
	rv := reflect.ValueOf(value)
	if !rv.IsValid() {
		return nil
	}
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil
		}
		return redactValue(key, rv.Elem().Interface())
	}
	if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
		out := make([]any, 0, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out = append(out, redactValue(key, rv.Index(i).Interface()))
		}
		return out
	}

	data, err := json.Marshal(value)
	if err != nil {
		return redactedValue
	}
	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return redactedValue
	}
	return redactValue(key, decoded)
}

func redactRawJSON(key string, raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return redactedValue
	}
	return redactValue(key, decoded)
}

func redactString(key, value string) string {
	if value == "" {
		return value
	}
	if looksLikeURL(value) {
		return redactURL(value)
	}
	if strings.Contains(strings.ToLower(key), "email") && strings.Contains(value, "@") {
		return maskEmail(value)
	}
	return emailPattern.ReplaceAllStringFunc(value, maskEmail)
}

func redactedForKey(key string, value any) any {
	normalized := strings.ToLower(strings.TrimSpace(key))
	if isPresenceSafeKey(normalized) {
		return redactValue("", value)
	}
	if strings.Contains(normalized, "email") {
		if text, ok := value.(string); ok {
			return maskEmail(text)
		}
	}
	if isSecretStateMap(value) {
		return redactValue("", value)
	}
	if isZeroValue(value) {
		return value
	}
	return redactedValue
}

func sensitiveFieldName(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if normalized == "" {
		return false
	}
	if isPresenceSafeKey(normalized) {
		return false
	}
	for _, exact := range []string{"raw_data", "raw_payload", "payload_json", "report_json"} {
		if normalized == exact {
			return true
		}
	}
	for _, marker := range []string{
		"password",
		"secret",
		"token",
		"license_key",
		"webhook_secret",
		"authorization",
		"cookie",
		"private_key",
		"client_secret",
		"api_key",
		"access_key",
		"refresh",
		"raw_metadata",
		"raw_payload",
		"blob",
		"payload",
		"email",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func isPresenceSafeKey(name string) bool {
	for _, suffix := range []string{"_set", "_state", "_present", "_enabled", "_configured", "_status", "_count"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	switch name {
	case "state", "status", "fingerprint", "enabled", "configured", "webhook_supported", "reconcile_supported":
		return true
	default:
		return false
	}
}

func isSecretStateMap(value any) bool {
	m, ok := value.(map[string]any)
	if !ok {
		return false
	}
	state, hasState := m["state"].(string)
	if !hasState {
		return false
	}
	state = strings.TrimSpace(state)
	if state != "present" && state != "missing" {
		return false
	}
	for key := range m {
		switch key {
		case "state", "fingerprint":
		default:
			return false
		}
	}
	return true
}

func isZeroValue(value any) bool {
	if value == nil {
		return true
	}
	rv := reflect.ValueOf(value)
	return rv.IsZero()
}

func maskEmail(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return value
	}
	at := strings.LastIndex(value, "@")
	if at <= 0 || at == len(value)-1 {
		return redactedValue
	}
	domain := value[at+1:]
	sum := sha256.Sum256([]byte(strings.ToLower(value)))
	return fmt.Sprintf("email:%s@%s", hex.EncodeToString(sum[:])[:10], domain)
}

func looksLikeURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && parsed.Scheme != "" && parsed.Host != ""
}

func redactURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return redactedValue
	}
	if parsed.User != nil {
		username := parsed.User.Username()
		if username == "" {
			parsed.User = url.User(redactedValue)
		} else if _, hasPassword := parsed.User.Password(); hasPassword {
			parsed.User = url.UserPassword(username, "redacted")
		} else {
			parsed.User = url.User(username)
		}
	}
	query := parsed.Query()
	for key := range query {
		if sensitiveURLQueryName(key) {
			query.Set(key, "redacted")
		}
	}
	parsed.RawQuery = query.Encode()
	return emailPattern.ReplaceAllStringFunc(parsed.String(), maskEmail)
}

func sensitiveURLQueryName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return strings.Contains(name, "password") ||
		strings.Contains(name, "secret") ||
		strings.Contains(name, "token") ||
		strings.Contains(name, "key")
}
