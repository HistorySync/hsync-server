package service

import (
	"strings"
	"testing"

	"github.com/historysync/hsync-server/pkg/provider"
)

func TestPasswordResetURL(t *testing.T) {
	svc := NewNotificationService(nil, provider.NewLogNotifier(), NotificationConfig{
		PublicURL:         "https://cloud.historysync.app/base/",
		PasswordResetPath: "/auth/reset",
	})

	got := svc.passwordResetURL("reset token")
	if !strings.HasPrefix(got, "https://cloud.historysync.app/base/auth/reset?") {
		t.Fatalf("passwordResetURL() = %q, want public URL plus reset path", got)
	}
	if !strings.Contains(got, "token=reset+token") {
		t.Fatalf("passwordResetURL() = %q, want encoded token query", got)
	}
}

func TestUsagePercent(t *testing.T) {
	if got := usagePercent(80, 100); got != 80 {
		t.Fatalf("usagePercent() = %d, want 80", got)
	}
	if got := usagePercent(150, 100); got != 100 {
		t.Fatalf("usagePercent() = %d, want capped 100", got)
	}
	if got := usagePercent(1, 0); got != 0 {
		t.Fatalf("usagePercent() = %d, want 0 for no limit", got)
	}
}

func TestQuotaIncreaseEvent(t *testing.T) {
	tests := []struct {
		name string
		old  int64
		new  int64
		want string
	}{
		{name: "below warning", old: 10, new: 79, want: ""},
		{name: "cross warning", old: 79, new: 80, want: "warning"},
		{name: "stay above warning", old: 80, new: 90, want: ""},
		{name: "cross exhausted", old: 99, new: 100, want: "exhausted"},
		{name: "jump to exhausted", old: 50, new: 100, want: "exhausted"},
		{name: "stay exhausted", old: 100, new: 100, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := quotaIncreaseEvent(tt.old, tt.new, 100, 80, 100); got != tt.want {
				t.Fatalf("quotaIncreaseEvent() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestQuotaRestored(t *testing.T) {
	if !quotaRestored(100, 99, 100, 100) {
		t.Fatal("quotaRestored() = false, want true when dropping below exhausted threshold")
	}
	if quotaRestored(99, 80, 100, 100) {
		t.Fatal("quotaRestored() = true, want false when not previously exhausted")
	}
	if quotaRestored(100, 100, 100, 100) {
		t.Fatal("quotaRestored() = true, want false when still exhausted")
	}
}
