// Package people keeps each teammate's own AI logins, so an AI call someone
// makes from the OS spends their Claude and Codex plans, not the owner's.
//
// The owner (the first FOUNDEROS_OWNER_EMAIL) runs on this Mac's own Claude
// and Codex logins, as before. Anyone else needs their own: a Claude token
// from `claude setup-token` and a Codex auth.json (or an OpenAI API key). A
// teammate without one gets ErrNoClaudeLogin, never the owner's login.
//
// Logins live under ~/.founderos/people/<user id>/ (FOUNDEROS_PEOPLE_DIR),
// directory 0700 and files 0600, like the rest of ~/.founderos. They are
// written here and read back to start a CLI or read that account's plan
// gauge; no route ever returns a saved token.
package people

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ErrNoClaudeLogin means a teammate asked for an AI call before adding their
// own Claude login.
var ErrNoClaudeLogin = errors.New("add your own Claude login first: Usage → My AI accounts")

// Person is who an OS request is for.
type Person struct {
	ID    string
	Email string
	Name  string
	// Owner runs on this Mac's own logins.
	Owner bool
}

type ctxKey struct{}

// With returns ctx carrying p.
func With(ctx context.Context, p Person) context.Context { return context.WithValue(ctx, ctxKey{}, p) }

// From is the person a request is for; false for background work (crons,
// syncs), which runs on this Mac's logins.
func From(ctx context.Context) (Person, bool) {
	p, ok := ctx.Value(ctxKey{}).(Person)
	return p, ok
}

// OwnerEmail is the first FOUNDEROS_OWNER_EMAIL: whose logins this Mac holds.
func OwnerEmail() string {
	for _, e := range strings.Split(os.Getenv("FOUNDEROS_OWNER_EMAIL"), ",") {
		if e = strings.TrimSpace(e); e != "" {
			return strings.ToLower(e)
		}
	}
	return ""
}

// IsOwner reports whether email is the owner's.
func IsOwner(email string) bool {
	o := OwnerEmail()
	return o != "" && strings.EqualFold(strings.TrimSpace(email), o)
}

// Root is where everyone's folders live.
func Root() string {
	if d := os.Getenv("FOUNDEROS_PEOPLE_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".founderos", "people")
}

var idJunk = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// Dir is one person's folder. Anything but [A-Za-z0-9_-] is dropped from
// the id, so an id can never point outside Root.
func Dir(id string) string {
	clean := idJunk.ReplaceAllString(id, "")
	if clean == "" {
		clean = "_"
	}
	return filepath.Join(Root(), clean)
}

func ensure(id string) (string, error) {
	d := Dir(id)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	_ = os.Chmod(Root(), 0o700)
	return d, os.Chmod(d, 0o700)
}

func writeSecret(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readTrim(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// ---- Claude ---------------------------------------------------------------

func claudeTokenPath(id string) string { return filepath.Join(Dir(id), "claude-token") }

// ClaudeConfigDir keeps a teammate's Claude Code state (and the transcripts
// it writes) apart from the owner's ~/.claude.
func ClaudeConfigDir(id string) string { return filepath.Join(Dir(id), "claude") }

// SaveClaudeToken stores the token `claude setup-token` printed.
func SaveClaudeToken(id, token string) error {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, "sk-ant-oat") || len(token) < 40 || strings.ContainsAny(token, " \n\t") {
		return errors.New("that is not a Claude login token: run `claude setup-token` and paste the line starting sk-ant-oat")
	}
	d, err := ensure(id)
	if err != nil {
		return err
	}
	return writeSecret(filepath.Join(d, "claude-token"), []byte(token+"\n"))
}

// ClaudeToken is the saved token, or "".
func ClaudeToken(id string) string { return readTrim(claudeTokenPath(id)) }

// ---- Codex ----------------------------------------------------------------

// CodexHome is a teammate's own CODEX_HOME.
func CodexHome(id string) string { return filepath.Join(Dir(id), "codex") }

// SaveCodexAPIKey stores an OpenAI API key (sk-…) as this person's Codex
// login. A ChatGPT-plan login goes through StartCodexLogin instead: a copied
// auth.json would share one refresh token between two machines.
func SaveCodexAPIKey(id, key string) error {
	key = strings.TrimSpace(key)
	if !strings.HasPrefix(key, "sk-") || len(key) < 20 || strings.ContainsAny(key, " \n\t") {
		return errors.New("that is not an OpenAI API key (they start sk-)")
	}
	if _, err := ensure(id); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(map[string]any{"OPENAI_API_KEY": key}, "", "  ")
	return writeSecret(filepath.Join(CodexHome(id), "auth.json"), raw)
}

// CodexKind is "chatgpt", "api key", or "" when none is saved.
func CodexKind(id string) string {
	raw, err := os.ReadFile(filepath.Join(CodexHome(id), "auth.json"))
	if err != nil {
		return ""
	}
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		return ""
	}
	if tok, ok := doc["tokens"].(map[string]any); ok && tok["refresh_token"] != nil {
		return "chatgpt"
	}
	if k, _ := doc["OPENAI_API_KEY"].(string); k != "" {
		return "api key"
	}
	return ""
}

// Forget removes one saved login: "claude" or "codex".
func Forget(id, provider string) error {
	switch provider {
	case "claude":
		err := os.Remove(claudeTokenPath(id))
		if os.IsNotExist(err) {
			return nil
		}
		return err
	case "codex":
		err := os.Remove(filepath.Join(CodexHome(id), "auth.json"))
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return errors.New("unknown provider " + provider)
}

// ---- running a CLI as someone ---------------------------------------------

// Env is the environment a CLI runs with for p: this process's own for the
// owner (and for background work), else base with the owner's logins taken
// out and p's put in. A teammate with no Claude token still gets their own
// empty CLAUDE_CONFIG_DIR and CODEX_HOME, so nothing falls back to the owner.
func Env(p Person, base []string) []string {
	if p.Owner {
		return base
	}
	drop := map[string]bool{"ANTHROPIC_API_KEY": true, "ANTHROPIC_AUTH_TOKEN": true, "CLAUDE_CODE_OAUTH_TOKEN": true,
		"CLAUDE_CONFIG_DIR": true, "CODEX_HOME": true, "OPENAI_API_KEY": true}
	out := make([]string, 0, len(base)+3)
	for _, kv := range base {
		if k, _, _ := strings.Cut(kv, "="); !drop[k] {
			out = append(out, kv)
		}
	}
	_ = os.MkdirAll(ClaudeConfigDir(p.ID), 0o700)
	_ = os.MkdirAll(CodexHome(p.ID), 0o700)
	out = append(out, "CLAUDE_CONFIG_DIR="+ClaudeConfigDir(p.ID), "CODEX_HOME="+CodexHome(p.ID))
	if t := ClaudeToken(p.ID); t != "" {
		out = append(out, "CLAUDE_CODE_OAUTH_TOKEN="+t)
	}
	return out
}

// ---- laptops --------------------------------------------------------------

// Laptop is a teammate's own Mac pushing their usage.
type Laptop struct {
	PersonID string    `json:"personId"`
	Email    string    `json:"email"`
	Name     string    `json:"name"`
	Hash     string    `json:"hash"`
	IssuedAt time.Time `json:"issuedAt"`
}

func laptopPath(id string) string { return filepath.Join(Dir(id), "laptop.json") }

func hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// IssueLaptopToken makes a new laptop token for p, replacing any earlier one
// (so an old laptop stops pushing). Only its hash is kept.
func IssueLaptopToken(p Person, now time.Time) (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := "tfgl_" + hex.EncodeToString(buf)
	if _, err := ensure(p.ID); err != nil {
		return "", err
	}
	raw, _ := json.Marshal(Laptop{PersonID: p.ID, Email: strings.ToLower(p.Email), Name: p.Name, Hash: hash(token), IssuedAt: now.UTC()})
	return token, writeSecret(laptopPath(p.ID), raw)
}

// LaptopFor is p's laptop record, if one was issued.
func LaptopFor(id string) (Laptop, bool) {
	raw, err := os.ReadFile(laptopPath(id))
	if err != nil {
		return Laptop{}, false
	}
	var l Laptop
	if json.Unmarshal(raw, &l) != nil || l.Hash == "" {
		return Laptop{}, false
	}
	return l, true
}

// RevokeLaptop stops p's laptop from pushing.
func RevokeLaptop(id string) error {
	err := os.Remove(laptopPath(id))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// LaptopByToken finds whose laptop token this is.
func LaptopByToken(token string) (Laptop, bool) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, "tfgl_") {
		return Laptop{}, false
	}
	want := hash(token)
	entries, err := os.ReadDir(Root())
	if err != nil {
		return Laptop{}, false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if l, ok := LaptopFor(e.Name()); ok && subtle.ConstantTimeCompare([]byte(l.Hash), []byte(want)) == 1 {
			return l, true
		}
	}
	return Laptop{}, false
}
