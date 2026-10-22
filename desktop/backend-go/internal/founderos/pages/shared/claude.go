package shared

import (
	"context"
	"os"
	"os/exec"

	"github.com/rhl/businessos-backend/internal/founderos/people"
)

// ClaudeCommand builds a `claude -p` call that can only produce text: no
// built-in tools (--tools ""), no MCP servers (--strict-mcp-config with none
// configured), run from a fresh empty directory. Prompts on the bridge carry
// third-party text, so the model must not be able to act (security review #2).
// Call cleanup when the command is done.
//
// A call made for a teammate (people.From(ctx)) runs on their own Claude
// login; one with no login saved is refused with people.ErrNoClaudeLogin
// rather than spending the owner's plan.
func ClaudeCommand(ctx context.Context, bin, prompt string) (*exec.Cmd, func(), error) {
	p, forSomeone := people.From(ctx)
	if forSomeone && !p.Owner && people.ClaudeToken(p.ID) == "" {
		return nil, func() {}, people.ErrNoClaudeLogin
	}
	dir, err := os.MkdirTemp("", "founderos-claude-")
	if err != nil {
		return nil, func() {}, err
	}
	cmd := exec.CommandContext(ctx, bin, "-p", prompt, "--output-format", "json", "--tools", "", "--strict-mcp-config")
	cmd.Dir = dir
	if forSomeone && !p.Owner {
		cmd.Env = people.Env(p, os.Environ())
	}
	return cmd, func() { _ = os.RemoveAll(dir) }, nil
}
