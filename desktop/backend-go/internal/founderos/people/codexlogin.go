package people

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// A Codex sign-in runs `codex login --device-auth` on this Mac with the
// teammate's own CODEX_HOME. The OS shows its link and one-time code; the
// teammate approves it in their own browser with their ChatGPT account, and
// Codex writes the login into their folder. Nobody copies a credential file.

// CodexLoginState is one sign-in as the page shows it.
type CodexLoginState struct {
	State     string    `json:"state"` // waiting | done | failed | none
	Link      string    `json:"link,omitempty"`
	Code      string    `json:"code,omitempty"`
	Error     string    `json:"error,omitempty"`
	StartedAt time.Time `json:"startedAt,omitempty"`
}

type codexLogin struct {
	CodexLoginState
	cancel context.CancelFunc
}

var (
	loginsMu sync.Mutex
	logins   = map[string]*codexLogin{}

	// CodexBin is the codex CLI; a var so tests run a fake.
	CodexBin = func() (string, error) { return exec.LookPath("codex") }
)

const codexLoginTimeout = 15 * time.Minute

var (
	ansi    = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	linkRe  = regexp.MustCompile(`https://\S+`)
	devCode = regexp.MustCompile(`\b[A-Z0-9]{4,5}-[A-Z0-9]{4,6}\b`)
)

// ParseDeviceAuth reads the link and code out of codex's device-auth output.
func ParseDeviceAuth(out string) (link, code string) {
	clean := ansi.ReplaceAllString(out, "")
	link = linkRe.FindString(clean)
	if i := strings.Index(clean, "code"); i >= 0 {
		code = devCode.FindString(clean[i:])
	}
	return link, code
}

// StartCodexLogin starts (or restarts) a sign-in for p and waits up to 20s
// for codex to print its link and code.
func StartCodexLogin(p Person) (CodexLoginState, error) {
	bin, err := CodexBin()
	if err != nil {
		return CodexLoginState{}, errors.New("codex is not installed on the OS host")
	}
	if _, err := ensure(p.ID); err != nil {
		return CodexLoginState{}, err
	}
	if err := os.MkdirAll(CodexHome(p.ID), 0o700); err != nil {
		return CodexLoginState{}, err
	}

	loginsMu.Lock()
	if old := logins[p.ID]; old != nil && old.cancel != nil {
		old.cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), codexLoginTimeout)
	l := &codexLogin{CodexLoginState: CodexLoginState{State: "waiting", StartedAt: time.Now().UTC()}, cancel: cancel}
	logins[p.ID] = l
	loginsMu.Unlock()

	cmd := exec.CommandContext(ctx, bin, "login", "--device-auth")
	cmd.Env = append(Env(p, os.Environ()), "NO_COLOR=1")
	cmd.Dir = CodexHome(p.ID)
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		cancel()
		return CodexLoginState{}, err
	}

	ready := make(chan struct{})
	go func() {
		var buf strings.Builder
		sc := bufio.NewScanner(pr)
		signalled := false
		for sc.Scan() {
			buf.WriteString(sc.Text() + "\n")
			if link, code := ParseDeviceAuth(buf.String()); link != "" && code != "" && !signalled {
				loginsMu.Lock()
				l.Link, l.Code = link, code
				loginsMu.Unlock()
				close(ready)
				signalled = true
			}
		}
		if !signalled {
			close(ready)
		}
	}()
	go func() {
		err := cmd.Wait()
		_ = pw.Close()
		cancel()
		loginsMu.Lock()
		defer loginsMu.Unlock()
		switch {
		case err == nil && CodexKind(p.ID) != "":
			l.State, l.Code = "done", ""
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			l.State, l.Error = "failed", "the code expired: start again"
		case l.State == "waiting":
			l.State, l.Error = "failed", "codex sign-in did not finish"
		}
	}()

	select {
	case <-ready:
	case <-time.After(20 * time.Second):
	}
	return CodexLogin(p.ID), nil
}

// CodexLogin is p's latest sign-in, or State "none".
func CodexLogin(id string) CodexLoginState {
	loginsMu.Lock()
	defer loginsMu.Unlock()
	if l := logins[id]; l != nil {
		return l.CodexLoginState
	}
	return CodexLoginState{State: "none"}
}
