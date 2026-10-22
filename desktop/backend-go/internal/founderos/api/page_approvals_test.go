package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rhl/businessos-backend/internal/founderos/connectors/paperclip"
	"github.com/rhl/businessos-backend/internal/founderos/guard"
)

type fakeApprovals struct {
	items             []paperclip.Interaction
	warnings          []string
	listErr, writeErr error
	accepted          []string
	choices           []paperclip.AcceptChoice
	rejected          []string
	answers           []paperclip.Answer
	verdicts          []paperclip.Verdict
	comment           string
}

func (f *fakeApprovals) PendingInteractions(context.Context) (*paperclip.Inbox, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return &paperclip.Inbox{Items: f.items, Warnings: f.warnings}, nil
}
func (f *fakeApprovals) IssueDocuments(_ context.Context, id string) ([]paperclip.Document, error) {
	if id == "bad" {
		return nil, paperclip.ErrBadID
	}
	return []paperclip.Document{{Key: "ledger", Title: "Ledger", Body: "rows"}}, nil
}
func (f *fakeApprovals) IssueThread(context.Context, string, int) ([]paperclip.Comment, error) {
	return []paperclip.Comment{{ID: "c1", Body: "handed over"}}, nil
}
func (f *fakeApprovals) AcceptInteraction(_ context.Context, issue, id string, choice paperclip.AcceptChoice) error {
	f.accepted = append(f.accepted, issue+"/"+id)
	f.choices = append(f.choices, choice)
	return f.writeErr
}
func (f *fakeApprovals) SubmitVerdicts(_ context.Context, issue, id string, v []paperclip.Verdict) error {
	f.verdicts = v
	return f.writeErr
}
func (f *fakeApprovals) RejectInteraction(_ context.Context, issue, id, reason string) error {
	f.rejected = append(f.rejected, issue+"/"+id+":"+reason)
	return f.writeErr
}
func (f *fakeApprovals) RespondInteraction(_ context.Context, issue, id string, a []paperclip.Answer) error {
	f.answers = a
	return f.writeErr
}
func (f *fakeApprovals) CommentOnIssue(_ context.Context, issue, msg string) (*paperclip.Comment, error) {
	f.comment = issue + ":" + msg
	return &paperclip.Comment{ID: "c2", Body: msg}, f.writeErr
}

func approvalsRouter(t *testing.T, f *fakeApprovals) http.Handler {
	t.Helper()
	prev := approvalsBoardFor
	approvalsBoardFor = func(*Deps) approvalsSource { return f }
	t.Cleanup(func() { approvalsBoardFor = prev })
	return router(t, &Deps{})
}

func TestApprovalsPageNeedsASession(t *testing.T) {
	h := approvalsRouter(t, &fakeApprovals{})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/founderos/pages/approvals", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no session: %d", w.Code)
	}
}

func TestApprovalsListsBoardRequestsAndReportsABoardOutage(t *testing.T) {
	f := &fakeApprovals{items: []paperclip.Interaction{{ID: "x1", IssueID: "i1", IssueIdentifier: "THE-9", AcceptLabel: "Approve completion"}},
		warnings: []string{"THE-15: requests could not be read"}}
	h := approvalsRouter(t, f)
	code, body := commsDo(t, h, http.MethodGet, "/api/founderos/pages/approvals", "")
	if code != http.StatusOK || len(body["items"].([]any)) != 1 || body["boardError"] != nil || len(body["warnings"].([]any)) != 1 {
		t.Fatalf("list: %d %v", code, body)
	}
	f.listErr = errors.New("board unreachable")
	code, body = commsDo(t, h, http.MethodGet, "/api/founderos/pages/approvals", "")
	if code != http.StatusOK || body["boardError"] != "board unreachable" || len(body["items"].([]any)) != 0 {
		t.Fatalf("outage: %d %v", code, body)
	}
}

func TestApprovalsDecideAnswerAndInstruct(t *testing.T) {
	f := &fakeApprovals{}
	h := approvalsRouter(t, f)
	if code, _ := commsDo(t, h, http.MethodPost, "/api/founderos/pages/approvals/issues/i1/interactions/x1/accept", "{}"); code != http.StatusOK || len(f.accepted) != 1 || f.accepted[0] != "i1/x1" {
		t.Fatalf("accept: %d %v", code, f.accepted)
	}
	if code, _ := commsDo(t, h, http.MethodPost, "/api/founderos/pages/approvals/issues/i1/interactions/x1/reject", `{"reason":"add sources"}`); code != http.StatusOK || f.rejected[0] != "i1/x1:add sources" {
		t.Fatalf("reject: %d %v", code, f.rejected)
	}
	if code, _ := commsDo(t, h, http.MethodPost, "/api/founderos/pages/approvals/issues/i3/interactions/x3/respond", `{"answers":[{"questionId":"q1","optionIds":["us"]}]}`); code != http.StatusOK || len(f.answers) != 1 || f.answers[0].OptionIDs[0] != "us" {
		t.Fatalf("respond: %d %v", code, f.answers)
	}
	if code, _ := commsDo(t, h, http.MethodPost, "/api/founderos/pages/approvals/issues/i3/interactions/x3/respond", `{"answers":[]}`); code != http.StatusOK {
		t.Fatalf("all-optional questions answered with none: %d", code)
	}
	if code, _ := commsDo(t, h, http.MethodPost, "/api/founderos/pages/approvals/issues/i3/interactions/x4/accept", `{"selectedClientKeys":["t2"]}`); code != http.StatusOK || f.choices[1].SelectedClientKeys[0] != "t2" {
		t.Fatalf("accept with tasks: %d %+v", code, f.choices)
	}
	if code, _ := commsDo(t, h, http.MethodPost, "/api/founderos/pages/approvals/issues/i3/interactions/x6/verdicts", `{"verdicts":[{"id":"d1","verdict":"reject","reason":"tone"}]}`); code != http.StatusOK || f.verdicts[0].Verdict != "reject" {
		t.Fatalf("verdicts: %d %+v", code, f.verdicts)
	}
	if code, _ := commsDo(t, h, http.MethodPost, "/api/founderos/pages/approvals/issues/i3/interactions/x6/verdicts", `{"verdicts":[{"id":"d1","verdict":"maybe"}]}`); code != http.StatusBadRequest {
		t.Fatalf("bad verdict: %d", code)
	}
	if code, _ := commsDo(t, h, http.MethodPost, "/api/founderos/pages/approvals/issues/i1/comment", `{"message":"  Please add sources.  "}`); code != http.StatusOK || f.comment != "i1:Please add sources." {
		t.Fatalf("comment: %d %q", code, f.comment)
	}
	if code, _ := commsDo(t, h, http.MethodPost, "/api/founderos/pages/approvals/issues/i1/comment", `{"message":"   "}`); code != http.StatusBadRequest {
		t.Fatalf("blank comment: %d", code)
	}
	if code, body := commsDo(t, h, http.MethodGet, "/api/founderos/pages/approvals/issues/i1", ""); code != http.StatusOK || len(body["documents"].([]any)) != 1 || len(body["thread"].([]any)) != 1 {
		t.Fatalf("issue context: %d %v", code, body)
	}
	if code, _ := commsDo(t, h, http.MethodGet, "/api/founderos/pages/approvals/issues/bad", ""); code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", code)
	}
}

func TestApprovalsReportGuardedWritesAsConflict(t *testing.T) {
	f := &fakeApprovals{writeErr: guard.ErrWritesDisabled}
	h := approvalsRouter(t, f)
	code, body := commsDo(t, h, http.MethodPost, "/api/founderos/pages/approvals/issues/i1/interactions/x1/accept", "{}")
	if code != http.StatusConflict || body["guarded"] != true {
		t.Fatalf("guarded: %d %v", code, body)
	}
}
