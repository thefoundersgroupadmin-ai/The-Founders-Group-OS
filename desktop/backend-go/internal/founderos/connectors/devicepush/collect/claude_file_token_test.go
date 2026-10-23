package collect

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWindowsClaudeLoginIsReadOnlyAndRereadAfterRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".credentials.json")
	source := claudeFileToken(path)
	if got := source(context.Background()); got != "" {
		t.Fatal("missing login must not produce a token")
	}
	for _, tc := range []struct {
		token  string
		expiry int64
		want   string
	}{
		{"first", time.Now().Add(time.Hour).UnixMilli(), "first"},
		{"rotated", time.Now().Add(time.Hour).UnixMilli(), "rotated"},
		{"expired", time.Now().Add(-time.Minute).UnixMilli(), ""},
	} {
		raw, _ := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{"accessToken": tc.token, "expiresAt": tc.expiry, "refreshToken": "do-not-use"}})
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if got := source(context.Background()); got != tc.want {
			t.Fatalf("token = %q, want %q", got, tc.want)
		}
		after, _ := os.ReadFile(path)
		if string(after) != string(raw) {
			t.Fatal("collector changed the Claude login")
		}
	}
}
