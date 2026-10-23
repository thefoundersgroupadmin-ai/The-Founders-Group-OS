package people

import (
	"context"
	"crypto/sha256"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rhl/businessos-backend/internal/founderos/connectors/devicepush"
	"github.com/rhl/businessos-backend/internal/founderos/connectors/devicepush/collect"
)

// Teammates' OS requests run here with their own CLAUDE_CONFIG_DIR. This is
// server-owned execution state, separate from device-local laptop pushes.
// Read only derived token totals, never transcript messages, into the board.
// The connected account can also supply its plan gauge on any browser/OS.
type personalClaudeReader struct {
	scan      *collect.ScanCache
	gauge     *collect.ClaudeGauge
	tokenHash [32]byte
}

var personalClaudeReaders = struct {
	sync.Mutex
	byDir map[string]*personalClaudeReader
}{byDir: map[string]*personalClaudeReader{}}

// ClaudeUsage reads the signed-in teammate's server-owned Claude activity and
// account plan gauge. It never falls back to the host owner's credentials.
func ClaudeUsage(ctx context.Context, now time.Time) *devicepush.SeatUsage {
	p, ok := From(ctx)
	if !ok || p.Owner {
		return nil
	}
	token := ClaudeToken(p.ID)
	if token == "" {
		return nil
	}
	dir := ClaudeConfigDir(p.ID)
	hash := sha256.Sum256([]byte(token))
	personalClaudeReaders.Lock()
	r := personalClaudeReaders.byDir[dir]
	if r == nil || r.tokenHash != hash {
		r = &personalClaudeReader{scan: collect.NewScanCache(), tokenHash: hash,
			gauge: collect.NewClaudeGauge(func(context.Context) string { return ClaudeToken(p.ID) })}
		// Bound cached readers, including old test roots and removed accounts.
		if len(personalClaudeReaders.byDir) >= 128 {
			personalClaudeReaders.byDir = map[string]*personalClaudeReader{}
		}
		personalClaudeReaders.byDir[dir] = r
	}
	personalClaudeReaders.Unlock()
	name := strings.TrimSpace(p.Name)
	if name == "" {
		name, _, _ = strings.Cut(p.Email, "@")
	}
	seat := collect.ScanClaudeProjects(filepath.Join(dir, "projects"), now, r.scan, collect.SeatInfo{
		ID: "claude-os-" + filepath.Base(Dir(p.ID)), Label: "Claude · " + name + "'s OS requests",
	})
	seat.Official = r.gauge.Read(ctx)
	seat.Note = "Token counts cover your Claude requests in TFG OS. Connect your laptop to include its local Claude Code activity."
	if seat.LastActivity == nil {
		seat.Note = "No Claude requests recorded in TFG OS yet. Connect your laptop to include its local Claude Code activity."
	}
	if seat.Official == nil {
		seat.Note += " Account limit percentages are unavailable for this login."
	} else {
		seat.Note += " Limit percentages come from your connected Claude account and refresh at most once a minute."
	}
	return &seat
}
