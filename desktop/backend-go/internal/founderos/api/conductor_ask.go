package api

// The Conductor answers directly: POST /pages/conductor/ask runs Claude (no
// tools, the asker's own login) with what is on the screen and a snapshot of
// the business, and replies in seconds. Work for the team is not done here:
// the model says DELEGATE and the OS files a Paperclip task on the right
// agent, so the board still does the work and keeps the record. Each person's
// conversation is kept under their people folder (conductor.json).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rhl/businessos-backend/internal/founderos/connectors/paperclip"
	"github.com/rhl/businessos-backend/internal/founderos/pages/conductor"
	"github.com/rhl/businessos-backend/internal/founderos/pages/shared"
	"github.com/rhl/businessos-backend/internal/founderos/people"
)

type askTurn struct {
	ID        string `json:"id"`
	Role      string `json:"role"` // user | assistant
	Content   string `json:"content"`
	CreatedAt string `json:"createdAt"`
	// Receipt is what the turn did: the task it filed, with a link.
	Receipt *struct {
		Text string `json:"text"`
		Href string `json:"href,omitempty"`
	} `json:"receipt,omitempty"`
}

const askKeep = 30

var askMu sync.Mutex

func askPath(id string) string { return filepath.Join(people.Dir(id), "conductor.json") }

func loadAsk(id string) []askTurn {
	var turns []askTurn
	if raw, err := os.ReadFile(askPath(id)); err == nil {
		_ = json.Unmarshal(raw, &turns)
	}
	if turns == nil {
		turns = []askTurn{}
	}
	return turns
}

func saveAsk(id string, turns []askTurn) {
	if len(turns) > askKeep {
		turns = turns[len(turns)-askKeep:]
	}
	raw, _ := json.Marshal(turns)
	_ = os.MkdirAll(filepath.Dir(askPath(id)), 0o700)
	_ = os.WriteFile(askPath(id), raw, 0o600)
}

// The team the Conductor can hand work to, by board name.
var delegableSeats = []string{"Prospect Research", "Sales Operations", "Sales Enablement and Delivery", "CEO"}

const conductorBrief = `You are the Conductor of TFG OS, the operating system of The Founders Group (a B2B sales consultancy: diagnose sales bottlenecks, build sales processes, coach the team through handover). You talk with %s, a member of the team, often by voice.

How to answer:
- Plain spoken English, short: one to four sentences unless they ask for detail. No markdown tables, no headings.
- Use only the facts below and the conversation. If you don't know, say so and say where in the OS to look. Never claim something happened that the facts don't show.
- You have no tools: never write tool calls or XML. You cannot click, send or change anything yourself.

Handing work to the team: when they ask for work to be done (research, drafting, analysis, a list, a plan), hand it to one agent: %s. End your reply with exactly one line:
DELEGATE {"agent": "<agent name>", "title": "<short task title>", "description": "<what to do, the outcome wanted, and any constraints>"}
Only delegate when asked for work, never for a question you can answer. Say in your reply who you gave it to.

What they are looking at: %s
%s

The business right now:
%s`

// askFacts is the snapshot the model answers from (each read is bounded).
func askFacts(ctx context.Context, d *Deps, now time.Time) string {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	var lines []string
	src := notifySourcesFor(d)
	var wg sync.WaitGroup
	var mu sync.Mutex
	add := func(s string) {
		mu.Lock()
		lines = append(lines, s)
		mu.Unlock()
	}
	wg.Add(3)
	go func() {
		defer wg.Done()
		add("- Today: " + strings.TrimPrefix(briefFrom(ctx, src, "", now), "Good morning. "))
	}()
	go func() {
		defer wg.Done()
		agents, err := paperclip.New(d.Resolver).Agents(ctx)
		if err != nil {
			add("- Agent board: unreachable right now")
			return
		}
		var parts []string
		for _, a := range agents {
			parts = append(parts, a.Name+" ("+a.Status+")")
		}
		add("- Agents: " + strings.Join(parts, ", "))
	}()
	go func() {
		defer wg.Done()
		ref := strings.TrimSpace(os.Getenv("FOUNDEROS_CAMPAIGN"))
		if ref == "" || d.Pool == nil {
			return
		}
		var sent, waiting int
		_ = d.Pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE o.status = 'sent'), count(*) FILTER (WHERE o.status = 'pending')
			FROM founderos_outbox o JOIN workspaces w ON w.id = o.workspace_id
			WHERE w.slug = $1 AND o.source = 'paperclip' AND o.source_ref = $2`, outboxWorkspace, ref).Scan(&sent, &waiting)
		state := "running"
		if why := campaignHalted(); why != "" {
			state = "stopped: " + why
		}
		add(fmt.Sprintf("- Outreach campaign %s (owner pre-approved, up to 50 first emails, 20 a day in US business hours): %d sent, %d drafted and waiting, %s", ref, sent, waiting, state))
	}()
	wg.Wait()
	if why := aiPaused(); why != "" {
		lines = append(lines, "- AI work is PAUSED: "+why)
	}
	return strings.Join(lines, "\n")
}

type delegation struct {
	Agent       string `json:"agent"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// splitDelegation takes the DELEGATE line off a reply.
func splitDelegation(reply string) (string, *delegation) {
	lines := strings.Split(strings.TrimSpace(reply), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(l, "DELEGATE") {
			continue
		}
		var dg delegation
		if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(l, "DELEGATE"))), &dg) != nil || dg.Title == "" || dg.Agent == "" {
			return reply, nil
		}
		rest := append(append([]string{}, lines[:i]...), lines[i+1:]...)
		return strings.TrimSpace(strings.Join(rest, "\n")), &dg
	}
	return reply, nil
}

// askClaude runs the model; swapped in tests.
// system replaces Claude Code's own system prompt, so the operator's personal
// CLAUDE.md and memory on this Mac never steer the Conductor into tool calls.
var askClaude = func(ctx context.Context, system, prompt string) (string, error) {
	bin, err := exec.LookPath("claude")
	if err != nil {
		for _, p := range []string{"/opt/homebrew/bin/claude", filepath.Join(os.Getenv("HOME"), ".local/bin/claude")} {
			if _, e := os.Stat(p); e == nil {
				bin, err = p, nil
				break
			}
		}
		if err != nil {
			return "", errors.New("the Claude command is not installed on the OS Mac")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	model := strings.TrimSpace(os.Getenv("FOUNDEROS_CONDUCTOR_MODEL"))
	if model == "" {
		model = "sonnet"
	}
	cmd, cleanup, err := shared.ClaudeCommand(ctx, bin, prompt, "--model", model, "--system-prompt", system, "--disable-slash-commands")
	if err != nil {
		return "", err
	}
	defer cleanup()
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(notifyClip(msg, 300))
	}
	var env struct {
		Result  string `json:"result"`
		IsError bool   `json:"is_error"`
	}
	if json.Unmarshal([]byte(out.String()), &env) != nil {
		return strings.TrimSpace(out.String()), nil
	}
	if env.IsError {
		return "", errors.New(notifyClip(env.Result, 300))
	}
	return strings.TrimSpace(env.Result), nil
}

type delegator interface {
	Agents(ctx context.Context) ([]paperclip.Agent, error)
	CreateIssue(ctx context.Context, title, description string) (*paperclip.Issue, error)
	AssignIssue(ctx context.Context, issueID, agentID string) error
}

var askBoardFor = func(d *Deps) delegator { return paperclip.New(d.Resolver) }

// fileDelegation puts the task on the board for the named seat.
func fileDelegation(ctx context.Context, b delegator, dg delegation, asker string) (*paperclip.Issue, string, error) {
	agents, err := b.Agents(ctx)
	if err != nil {
		return nil, "", err
	}
	var seat *paperclip.Agent
	for i := range agents {
		if strings.EqualFold(agents[i].Name, strings.TrimSpace(dg.Agent)) {
			seat = &agents[i]
		}
	}
	if seat == nil {
		return nil, "", fmt.Errorf("no agent called %q on the board", dg.Agent)
	}
	desc := strings.TrimSpace(dg.Description) + "\n\nRequested by " + asker + " through the TFG OS Conductor. " +
		"Your standing boundaries apply: no outreach, sending, payments or spending unless a task says the owner pre-approved it."
	is, err := b.CreateIssue(ctx, notifyClip(dg.Title, 120), desc)
	if err != nil {
		return nil, "", err
	}
	if is == nil {
		return nil, "", errors.New("the board did not return the task")
	}
	if err := b.AssignIssue(ctx, is.ID, seat.ID); err != nil {
		return is, seat.Name, err
	}
	return is, seat.Name, nil
}

func registerConductorAsk(s gin.IRoutes, d *Deps) {
	s.GET("/pages/conductor/ask", func(c *gin.Context) {
		p, ok := personOf(c)
		if !ok {
			return
		}
		askMu.Lock()
		defer askMu.Unlock()
		c.JSON(http.StatusOK, gin.H{"turns": loadAsk(p.ID)})
	})
	s.DELETE("/pages/conductor/ask", func(c *gin.Context) {
		p, ok := personOf(c)
		if !ok {
			return
		}
		askMu.Lock()
		defer askMu.Unlock()
		saveAsk(p.ID, []askTurn{})
		c.JSON(http.StatusOK, gin.H{"turns": []askTurn{}})
	})
	s.POST("/pages/conductor/ask", func(c *gin.Context) {
		p, ok := personOf(c)
		if !ok {
			return
		}
		var body struct {
			Message string `json:"message"`
			Path    string `json:"path"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Message) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "message required"})
			return
		}
		if why := aiPaused(); why != "" {
			c.JSON(http.StatusConflict, gin.H{"error": "AI work is paused: " + why})
			return
		}
		ctx := c.Request.Context()
		now := time.Now()
		path := body.Path
		if path == "" {
			path = "/os"
		}
		title := conductor.ScreenTitle(path)
		sctx, cancel := context.WithTimeout(ctx, contextBudget)
		screen := screenContext(sctx, d, path, title)
		cancel()

		askMu.Lock()
		history := loadAsk(p.ID)
		askMu.Unlock()
		var convo strings.Builder
		start := 0
		if len(history) > 12 {
			start = len(history) - 12
		}
		for _, t := range history[start:] {
			who := "They"
			if t.Role == "assistant" {
				who = "You"
			}
			convo.WriteString(who + ": " + t.Content + "\n")
		}
		name := p.Name
		if name == "" {
			name = p.Email
		}
		system := fmt.Sprintf(conductorBrief, name, strings.Join(delegableSeats, ", "), title, screen, askFacts(ctx, d, now))
		prompt := "The conversation so far:\n" + convo.String() + "They: " + strings.TrimSpace(body.Message) + "\nYou:"

		reply, err := askClaude(ctx, system, prompt)
		if err != nil {
			status := http.StatusBadGateway
			if errors.Is(err, people.ErrNoClaudeLogin) {
				status = http.StatusConflict
			}
			c.JSON(status, gin.H{"error": err.Error()})
			return
		}
		text, dg := splitDelegation(reply)
		user := askTurn{ID: fmt.Sprintf("u-%d", now.UnixNano()), Role: "user", Content: strings.TrimSpace(body.Message), CreatedAt: now.UTC().Format(time.RFC3339Nano)}
		answer := askTurn{ID: fmt.Sprintf("a-%d", time.Now().UnixNano()), Role: "assistant", Content: text, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		if dg != nil {
			is, seat, err := fileDelegation(ctx, askBoardFor(d), *dg, name)
			answer.Receipt = &struct {
				Text string `json:"text"`
				Href string `json:"href,omitempty"`
			}{}
			switch {
			case err != nil && is == nil:
				answer.Receipt.Text = "Could not file the task: " + err.Error()
			case err != nil:
				answer.Receipt.Text = fmt.Sprintf("Filed %s, but could not assign it to %s: %v", is.Identifier, dg.Agent, err)
				answer.Receipt.Href = "/os/agents"
			default:
				answer.Receipt.Text = fmt.Sprintf("Filed %s for %s: %s", is.Identifier, seat, is.Title)
				answer.Receipt.Href = "/os/agents"
			}
		}
		askMu.Lock()
		history = append(loadAsk(p.ID), user, answer)
		saveAsk(p.ID, history)
		askMu.Unlock()
		c.JSON(http.StatusOK, gin.H{"reply": answer, "turns": history})
	})
}
