package paperclip

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/rhl/businessos-backend/internal/founderos/guard"
)

type approvalsBoard struct {
	mu    sync.Mutex
	posts map[string]string
	// extraIssues pads the task list so the scan must page.
	extraIssues int
}

// Interaction payloads shaped like Paperclip's validators (packages/shared/src/validators/issue.ts).
const (
	reviewJSON = `{"id":"x1","kind":"request_confirmation","status":"pending","title":"Review requested","createdAt":"2026-10-21T18:46:00Z",
		"payload":{"version":1,"prompt":"Accept revision 4?","acceptLabel":"Approve completion","rejectLabel":"Continue work","rejectRequiresReason":true,"detailsMarkdown":"30 rows"}}`
	questionJSON = `{"id":"x3","kind":"ask_user_questions","status":"pending","createdAt":"2026-10-21T10:00:00Z",
		"payload":{"version":1,"title":"Pick a market","questions":[
			{"id":"q1","prompt":"Which first?","selectionMode":"single","options":[{"id":"us","label":"US"},{"id":"desc","label":"I'll describe it","freeText":true}]},
			{"id":"q2","prompt":"Anything else?","selectionMode":"multi","required":false,"allowOther":true,"options":[{"id":"none","label":"No"}]}]}}`
	tasksJSON = `{"id":"x4","kind":"suggest_tasks","status":"pending","createdAt":"2026-10-21T11:00:00Z",
		"payload":{"version":1,"tasks":[{"clientKey":"t1","title":"Draft emails"},{"clientKey":"t2","title":"Find addresses","description":"public only"}]}}`
	checkboxJSON = `{"id":"x5","kind":"request_checkbox_confirmation","status":"pending","createdAt":"2026-10-21T12:00:00Z",
		"payload":{"version":1,"prompt":"Which channels?","options":[{"id":"email","label":"Email"},{"id":"li","label":"LinkedIn","description":"manual"}],
			"defaultSelectedOptionIds":["email"],"minSelected":1,"maxSelected":2}}`
	verdictsJSON = `{"id":"x6","kind":"request_item_verdicts","status":"pending","createdAt":"2026-10-21T13:00:00Z",
		"payload":{"version":1,"prompt":"Judge each draft","items":[{"id":"d1","label":"Acme","previewMarkdown":"Hi Ann"},{"id":"d2","label":"Beta"}],"verdicts":["approve","reject","defer"],"requireReasonOn":["reject"]},
		"result":{"version":1,"outcome":"resolved","complete":false,"items":[{"id":"d2","verdict":"reject","reason":"off-brand","resolvedAt":"2026-10-21T13:05:00Z"}]}}`
)

func newApprovalsBoard(t *testing.T) (*Connector, *approvalsBoard) {
	t.Helper()
	ab := &approvalsBoard{posts: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer board-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/companies/c1/issues":
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			all := []string{
				`{"id":"i2","identifier":"THE-4","title":"Done one","status":"done"}`,
				`{"id":"i3","identifier":"THE-14","title":"Question","status":"todo"}`,
				`{"id":"i9","identifier":"THE-15","title":"Flaky","status":"todo"}`,
			}
			for n := 0; n < ab.extraIssues; n++ {
				all = append(all, fmt.Sprintf(`{"id":"p%d","identifier":"THE-P%d","title":"padding","status":"done"}`, n, n))
			}
			// The review lives on the very last task, beyond the first page.
			all = append(all, `{"id":"i1","identifier":"THE-9","title":"US list","status":"in_review"}`)
			end := offset + limit
			if end > len(all) {
				end = len(all)
			}
			if offset > len(all) {
				offset = len(all)
			}
			io.WriteString(w, "["+strings.Join(all[offset:end], ",")+"]")
		case r.Method == http.MethodGet && r.URL.Path == "/api/issues/i1/interactions":
			io.WriteString(w, `[`+reviewJSON+`,{"id":"x0","kind":"request_confirmation","status":"accepted","title":"Old"}]`)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/issues/p"):
			t.Error("a closed task was polled for interactions")
		case r.Method == http.MethodGet && r.URL.Path == "/api/issues/i2/interactions":
			t.Error("a closed task was polled for interactions")
		case r.Method == http.MethodGet && r.URL.Path == "/api/issues/i3/interactions":
			io.WriteString(w, `{"interactions":[`+questionJSON+`,`+tasksJSON+`,`+checkboxJSON+`,`+verdictsJSON+`]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/issues/i9/interactions":
			w.WriteHeader(http.StatusInternalServerError)
		case r.Method == http.MethodGet && r.URL.Path == "/api/issues/i1/documents":
			io.WriteString(w, `[{"key":"ledger","title":"Ledger","latestRevision":{"body":"| a | b |"},"updatedAt":"2026-10-21"}]`)
		case r.Method == http.MethodPost:
			b, _ := io.ReadAll(r.Body)
			ab.mu.Lock()
			ab.posts[r.URL.Path] = string(b)
			ab.mu.Unlock()
			io.WriteString(w, `{"id":"cm1","body":"ok","createdAt":"2026-10-22"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return New(resolver(t, "PAPERCLIP_API_URL="+srv.URL+"/", "PAPERCLIP_BOARD_KEY=board-key", "PAPERCLIP_COMPANY_ID=c1")), ab
}

func byID(items []Interaction) map[string]Interaction {
	m := map[string]Interaction{}
	for _, it := range items {
		m[it.ID] = it
	}
	return m
}

func TestPendingInteractionsListsEveryKindAcrossPagesAndWarnsOnFailures(t *testing.T) {
	c, ab := newApprovalsBoard(t)
	ab.extraIssues = 450 // the review sits on the third page of tasks
	box, err := c.PendingInteractions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(box.Items) != 5 {
		t.Fatalf("want all 5 pending requests (every kind), got %d: %+v", len(box.Items), box.Items)
	}
	if box.Items[0].ID != "x3" || box.Items[4].ID != "x1" {
		t.Errorf("not oldest first: %s .. %s", box.Items[0].ID, box.Items[4].ID)
	}
	if len(box.Warnings) != 1 || !strings.HasPrefix(box.Warnings[0], "THE-15:") {
		t.Errorf("a task whose requests failed must be named, got %v", box.Warnings)
	}
	m := byID(box.Items)
	if r := m["x1"]; r.AcceptLabel != "Approve completion" || !r.RejectRequiresReason || r.IssueIdentifier != "THE-9" {
		t.Errorf("review = %+v", r)
	}
	q := m["x3"]
	if len(q.Questions) != 2 || !q.Questions[0].Required || q.Questions[1].Required || !q.Questions[1].AllowOther || !q.Questions[0].Options[1].FreeText {
		t.Errorf("questions = %+v", q.Questions)
	}
	if s := m["x4"]; len(s.Tasks) != 2 || s.Tasks[1].ClientKey != "t2" || s.Tasks[1].Description != "public only" {
		t.Errorf("suggested tasks = %+v", s.Tasks)
	}
	if cb := m["x5"]; len(cb.Checkboxes) != 2 || cb.DefaultSelected[0] != "email" || cb.MinSelected != 1 || cb.MaxSelected == nil || *cb.MaxSelected != 2 {
		t.Errorf("checkboxes = %+v", cb)
	}
	if v := m["x6"]; len(v.Items) != 2 || v.Items[0].Preview != "Hi Ann" || v.Items[0].Resolved != nil || len(v.Verdicts) != 3 || v.RequireReasonOn[0] != "reject" {
		t.Errorf("verdicts = %+v", v)
	}
	if d2 := m["x6"].Items[1]; d2.Resolved == nil || *d2.Resolved != "reject" || d2.ResolvedReason == nil || *d2.ResolvedReason != "off-brand" {
		t.Errorf("an already-judged item must carry its recorded verdict: %+v", d2)
	}
}

func TestIssueDocumentsFallsBackToTheLatestRevision(t *testing.T) {
	c, _ := newApprovalsBoard(t)
	docs, err := c.IssueDocuments(context.Background(), "i1")
	if err != nil || len(docs) != 1 || docs[0].Body != "| a | b |" || docs[0].Title != "Ledger" {
		t.Fatalf("docs=%+v err=%v", docs, err)
	}
}

func TestDecisionsPostSchemaShapedBodiesAndAreGuarded(t *testing.T) {
	c, ab := newApprovalsBoard(t)
	ctx := context.Background()

	t.Setenv("FOUNDEROS_WRITES", "0")
	guard.Reset()
	if err := c.AcceptInteraction(ctx, "i1", "x1", AcceptChoice{}); !errors.Is(err, guard.ErrWritesDisabled) {
		t.Fatalf("writes off: err = %v", err)
	}

	t.Setenv("FOUNDEROS_WRITES", "1")
	other := "Start with fintech"
	reason := "  tone is off  "
	for _, step := range []func() error{
		func() error { return c.AcceptInteraction(ctx, "i1", "x1", AcceptChoice{}) },
		func() error { return c.RejectInteraction(ctx, "i1", "x1", "  add sources  ") },
		func() error {
			return c.RespondInteraction(ctx, "i3", "x3", []Answer{{QuestionID: "q1", OptionIDs: []string{"desc"}, OtherText: &other}})
		},
		func() error {
			return c.AcceptInteraction(ctx, "i3", "x4", AcceptChoice{SelectedClientKeys: []string{"t2"}})
		},
		func() error {
			return c.AcceptInteraction(ctx, "i3", "x5", AcceptChoice{SelectedOptionIDs: []string{"email", "li"}})
		},
		func() error {
			return c.SubmitVerdicts(ctx, "i3", "x6", []Verdict{{ID: "d1", Verdict: "reject", Reason: &reason}})
		},
		func() error { _, err := c.CommentOnIssue(ctx, "i1", "Please add sources."); return err },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	ab.mu.Lock()
	defer ab.mu.Unlock()
	body := func(path string) map[string]any {
		var m map[string]any
		if err := json.Unmarshal([]byte(ab.posts[path]), &m); err != nil {
			t.Fatalf("%s: %q", path, ab.posts[path])
		}
		return m
	}
	if b := body("/api/issues/i1/interactions/x1/accept"); len(b) != 0 {
		t.Errorf("plain accept body = %v", b)
	}
	if b := body("/api/issues/i1/interactions/x1/reject"); b["reason"] != "add sources" {
		t.Errorf("reject = %v", b)
	}
	if !strings.Contains(ab.posts["/api/issues/i3/interactions/x3/respond"], `"otherText":"Start with fintech"`) {
		t.Errorf("respond = %s", ab.posts["/api/issues/i3/interactions/x3/respond"])
	}
	if !strings.Contains(ab.posts["/api/issues/i3/interactions/x4/accept"], `"selectedClientKeys":["t2"]`) ||
		!strings.Contains(ab.posts["/api/issues/i3/interactions/x5/accept"], `"selectedOptionIds":["email","li"]`) {
		t.Errorf("accept choices = %s / %s", ab.posts["/api/issues/i3/interactions/x4/accept"], ab.posts["/api/issues/i3/interactions/x5/accept"])
	}
	if !strings.Contains(ab.posts["/api/issues/i3/interactions/x6/verdicts"], `{"id":"d1","reason":"tone is off","verdict":"reject"}`) {
		t.Errorf("verdicts = %s", ab.posts["/api/issues/i3/interactions/x6/verdicts"])
	}
	if !strings.Contains(ab.posts["/api/issues/i1/comments"], "Please add sources.") {
		t.Errorf("comment = %s", ab.posts["/api/issues/i1/comments"])
	}
}

func TestIDsThatCouldEscapeThePathAreRefused(t *testing.T) {
	c, _ := newApprovalsBoard(t)
	t.Setenv("FOUNDEROS_WRITES", "1")
	for _, id := range []string{"", ".", "..", "../companies", "a/b", "x?y", "%2e%2e"} {
		if err := c.AcceptInteraction(context.Background(), "i1", id, AcceptChoice{}); !errors.Is(err, ErrBadID) {
			t.Errorf("interaction id %q: err = %v", id, err)
		}
		if _, err := c.IssueDocuments(context.Background(), id); !errors.Is(err, ErrBadID) {
			t.Errorf("issue id %q: err = %v", id, err)
		}
	}
}

func TestTheTaskScanWarnsWhenItHitsThePageCap(t *testing.T) {
	c, ab := newApprovalsBoard(t)
	ab.extraIssues = issuePage*issuePageLimit + 10 // more tasks than the scan reads
	box, err := c.PendingInteractions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	last := box.Warnings[len(box.Warnings)-1]
	if !strings.HasPrefix(last, "Only the first 5000 tasks were checked") {
		t.Fatalf("warnings = %v", box.Warnings)
	}
}
