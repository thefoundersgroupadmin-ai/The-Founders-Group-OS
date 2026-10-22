package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/rhl/businessos-backend/internal/founderos/connectors/paperclip"
)

type fakeDelegator struct {
	created  []string
	assigned []string
}

func (f *fakeDelegator) Agents(context.Context) ([]paperclip.Agent, error) {
	return []paperclip.Agent{{ID: "pr-1", Name: "Prospect Research"}, {ID: "ceo-1", Name: "CEO"}}, nil
}
func (f *fakeDelegator) CreateIssue(_ context.Context, title, desc string) (*paperclip.Issue, error) {
	f.created = append(f.created, title+"\n"+desc)
	return &paperclip.Issue{ID: "i-9", Identifier: "THE-40", Title: title}, nil
}
func (f *fakeDelegator) AssignIssue(_ context.Context, issue, agent string) error {
	f.assigned = append(f.assigned, issue+"→"+agent)
	return nil
}

func TestTheDelegateLineComesOffTheSpokenReply(t *testing.T) {
	text, dg := splitDelegation("Sure, I'll have Prospect Research find them.\nDELEGATE {\"agent\": \"Prospect Research\", \"title\": \"Find 20 fintech leads\", \"description\": \"US Series A\"}")
	if text != "Sure, I'll have Prospect Research find them." || dg == nil || dg.Agent != "Prospect Research" || dg.Title != "Find 20 fintech leads" {
		t.Fatalf("%q %+v", text, dg)
	}
	if text, dg := splitDelegation("You have two calls today."); dg != nil || text != "You have two calls today." {
		t.Fatalf("no delegation: %q %+v", text, dg)
	}
	if _, dg := splitDelegation("DELEGATE {broken"); dg != nil {
		t.Fatal("a broken line must not file anything")
	}
}

func TestTheConductorAnswersAtOnceAndHandsWorkToTheBoard(t *testing.T) {
	peopleSetup(t)
	var prompts []string
	old := askClaude
	askClaude = func(_ context.Context, system, prompt string) (string, error) {
		prompts = append(prompts, system+"\n"+prompt)
		if strings.Contains(prompt, "They: find me 20 fintech leads") {
			return "I've asked Prospect Research to find them.\nDELEGATE {\"agent\": \"Prospect Research\", \"title\": \"Find 20 fintech leads\", \"description\": \"US Series A fintech\"}", nil
		}
		return "You have two calls today.", nil
	}
	defer func() { askClaude = old }()
	board := &fakeDelegator{}
	oldBoard := askBoardFor
	askBoardFor = func(*Deps) delegator { return board }
	defer func() { askBoardFor = oldBoard }()
	r := asUser(t, &Deps{}, karim)

	w := do(t, r, http.MethodPost, "/api/founderos/pages/conductor/ask", `{"message":"how many calls today?","path":"/os/funnel"}`)
	var got struct {
		Reply askTurn   `json:"reply"`
		Turns []askTurn `json:"turns"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Reply.Content != "You have two calls today." || len(got.Turns) != 2 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if !strings.Contains(prompts[0], "Karim") || !strings.Contains(prompts[0], "What they are looking at:") {
		t.Fatalf("prompt lacks the asker or the screen:\n%s", prompts[0])
	}

	w = do(t, r, http.MethodPost, "/api/founderos/pages/conductor/ask", `{"message":"find me 20 fintech leads"}`)
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got.Reply.Content != "I've asked Prospect Research to find them." || got.Reply.Receipt == nil || !strings.HasPrefix(got.Reply.Receipt.Text, "Filed THE-40 for Prospect Research") {
		t.Fatalf("delegation reply: %s", w.Body)
	}
	if len(board.assigned) != 1 || board.assigned[0] != "i-9→pr-1" || !strings.Contains(board.created[0], "no outreach, sending, payments or spending") {
		t.Fatalf("board: %+v", board)
	}
	if !strings.Contains(prompts[1], "They: how many calls today?") || !strings.Contains(prompts[1], "You: You have two calls today.") {
		t.Fatal("the conversation must carry over")
	}

	// History is per person.
	w = do(t, asUser(t, &Deps{}, ownerUser), http.MethodGet, "/api/founderos/pages/conductor/ask", "")
	if !strings.Contains(w.Body.String(), `"turns":[]`) {
		t.Fatalf("someone else's conversation leaked: %s", w.Body)
	}
	if w := do(t, r, http.MethodDelete, "/api/founderos/pages/conductor/ask", ""); !strings.Contains(w.Body.String(), `"turns":[]`) {
		t.Fatalf("clear: %s", w.Body)
	}
}

func TestTheConductorSaysWhyItCannotAnswer(t *testing.T) {
	peopleSetup(t)
	old := askClaude
	askClaude = func(context.Context, string, string) (string, error) { return "", errors.New("Invalid API key") }
	defer func() { askClaude = old }()
	w := do(t, asUser(t, &Deps{}, karim), http.MethodPost, "/api/founderos/pages/conductor/ask", `{"message":"hi"}`)
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "Invalid API key") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}
