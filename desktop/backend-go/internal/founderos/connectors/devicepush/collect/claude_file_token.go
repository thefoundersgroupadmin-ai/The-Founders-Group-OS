package collect

import (
	"context"
	"io"
	"os"
	"time"
)

// Windows Claude Code keeps its own OAuth login in .credentials.json. Borrow
// only the current unexpired access token for the plan gauge. Never refresh
// or modify the login, and never include it in a pushed payload. Read again
// each tick so Claude Code's own credential rotation takes effect.
func claudeFileToken(path string) TokenSource {
	return func(ctx context.Context) string {
		if ctx.Err() != nil {
			return ""
		}
		f, err := os.Open(path)
		if err != nil {
			return ""
		}
		defer f.Close()
		raw, err := io.ReadAll(io.LimitReader(f, 1<<20))
		if err != nil {
			return ""
		}
		return AccessTokenFromKeychainJSON(string(raw), time.Now().UnixMilli())
	}
}
