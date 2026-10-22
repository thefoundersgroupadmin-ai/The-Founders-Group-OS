package paperclip

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
)

// Approvals: what the board is waiting on the operator for (pending issue
// thread interactions: review requests and agent questions), plus the reads
// that let the OS show the work being reviewed. Decisions and comments are
// guarded writes like every other board write.

// Option is one choice of an agent's question. FreeText reveals a text box
// whose value is returned as the answer's OtherText.
type Option struct {
	ID          string  `json:"id"`
	Label       string  `json:"label"`
	Description *string `json:"description"`
	FreeText    bool    `json:"freeText"`
}

// Question is one ask_user_questions entry.
type Question struct {
	ID            string   `json:"id"`
	Prompt        string   `json:"prompt"`
	HelpText      *string  `json:"helpText"`
	SelectionMode string   `json:"selectionMode"`
	Required      bool     `json:"required"`
	AllowOther    bool     `json:"allowOther"`
	Options       []Option `json:"options"`
}

// SuggestedTask is one task an agent proposes creating (suggest_tasks).
type SuggestedTask struct {
	ClientKey   string `json:"clientKey"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// VerdictItem is one item to judge (request_item_verdicts). Resolved is the
// verdict already recorded for it (from the interaction's result), if any:
// the board keeps that one, so the OS must not offer it again.
type VerdictItem struct {
	ID             string  `json:"id"`
	Label          string  `json:"label"`
	Description    *string `json:"description"`
	Preview        string  `json:"preview"`
	Resolved       *string `json:"resolved"`
	ResolvedReason *string `json:"resolvedReason"`
}

// Interaction is one pending request on a board task.
type Interaction struct {
	ID                   string     `json:"id"`
	IssueID              string     `json:"issueId"`
	IssueIdentifier      string     `json:"issueIdentifier"`
	IssueTitle           string     `json:"issueTitle"`
	Kind                 string     `json:"kind"`
	Title                string     `json:"title"`
	Prompt               string     `json:"prompt"`
	Details              string     `json:"details"`
	AcceptLabel          string     `json:"acceptLabel"`
	RejectLabel          string     `json:"rejectLabel"`
	RejectRequiresReason bool       `json:"rejectRequiresReason"`
	Questions            []Question `json:"questions"`
	// request_checkbox_confirmation
	Checkboxes      []Option `json:"checkboxes"`
	DefaultSelected []string `json:"defaultSelected"`
	MinSelected     int      `json:"minSelected"`
	MaxSelected     *int     `json:"maxSelected"`
	// suggest_tasks
	Tasks []SuggestedTask `json:"tasks"`
	// request_item_verdicts
	Items           []VerdictItem `json:"items"`
	Verdicts        []string      `json:"verdicts"`
	RequireReasonOn []string      `json:"requireReasonOn"`
	CreatedAt       string        `json:"createdAt"`
}

// Inbox is the pending list plus any tasks whose requests could not be read,
// so a partial board outage is shown, not mistaken for an empty queue.
type Inbox struct {
	Items    []Interaction `json:"items"`
	Warnings []string      `json:"warnings"`
}

// AcceptChoice carries the selections some kinds need on accept: which
// suggested tasks to create, which checkboxes are ticked.
type AcceptChoice struct {
	SelectedClientKeys []string `json:"selectedClientKeys"`
	SelectedOptionIDs  []string `json:"selectedOptionIds"`
}

// Verdict is the operator's call on one item.
type Verdict struct {
	ID      string  `json:"id"`
	Verdict string  `json:"verdict"`
	Reason  *string `json:"reason"`
}

// Document is one task document (e.g. a deliverable under review).
type Document struct {
	Key       string `json:"key"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	UpdatedAt string `json:"updatedAt"`
}

// Answer is the operator's choice for one question; OtherText is the typed
// value of a free-text or "other" option.
type Answer struct {
	QuestionID string   `json:"questionId"`
	OptionIDs  []string `json:"optionIds"`
	OtherText  *string  `json:"otherText"`
}

// ErrBadID guards path segments built from caller input.
var ErrBadID = errors.New("paperclip: invalid id")

// closedStatuses never carry a live request worth asking about.
var closedStatuses = map[string]bool{"done": true, "cancelled": true}

// issuePage and issuePageLimit bound the task scan (offset paging).
const (
	issuePage      = 200
	issuePageLimit = 25
)

func validID(id string) bool {
	return id != "" && id != "." && id != ".." && len(id) <= 200 && url.PathEscape(id) == id && !strings.Contains(id, "/")
}

func strList(v any) []string {
	out := []string{}
	for _, x := range listFrom(v, "") {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func mapOptions(v any) []Option {
	out := []Option{}
	for _, o := range listFrom(v, "") {
		om := obj(o)
		opt := Option{ID: strOf(om, "id"), Label: strOf(om, "label"), Description: str(om["description"])}
		if b, ok := om["freeText"].(bool); ok {
			opt.FreeText = b
		}
		out = append(out, opt)
	}
	return out
}

func strOf(m map[string]any, k string) string {
	if p := str(m[k]); p != nil {
		return *p
	}
	return ""
}

// MapInteraction shapes one board interaction for the OS.
func MapInteraction(raw any, issue Issue) Interaction {
	m := obj(raw)
	p := obj(m["payload"])
	it := Interaction{
		ID: strOf(m, "id"), IssueID: issue.ID, IssueIdentifier: issue.Identifier, IssueTitle: issue.Title,
		Kind: strOf(m, "kind"), Title: strOf(m, "title"), CreatedAt: strOf(m, "createdAt"),
		Prompt: strOf(p, "prompt"), Details: strOf(p, "detailsMarkdown"),
		AcceptLabel: strOf(p, "acceptLabel"), RejectLabel: strOf(p, "rejectLabel"),
		Questions: []Question{}, Checkboxes: []Option{}, DefaultSelected: strList(p["defaultSelectedOptionIds"]),
		Tasks: []SuggestedTask{}, Items: []VerdictItem{}, Verdicts: strList(p["verdicts"]), RequireReasonOn: strList(p["requireReasonOn"]),
	}
	if n, ok := p["minSelected"].(float64); ok {
		it.MinSelected = int(n)
	}
	if n, ok := p["maxSelected"].(float64); ok {
		v := int(n)
		it.MaxSelected = &v
	}
	if it.Kind == "request_checkbox_confirmation" {
		it.Checkboxes = mapOptions(p["options"])
	}
	for _, t := range listFrom(p["tasks"], "") {
		tm := obj(t)
		it.Tasks = append(it.Tasks, SuggestedTask{ClientKey: strOf(tm, "clientKey"), Title: strOf(tm, "title"), Description: strOf(tm, "description")})
	}
	resolved := map[string]map[string]any{}
	for _, r := range listFrom(obj(m["result"])["items"], "") {
		rm := obj(r)
		resolved[strOf(rm, "id")] = rm
	}
	for _, x := range listFrom(p["items"], "") {
		xm := obj(x)
		vi := VerdictItem{ID: strOf(xm, "id"), Label: strOf(xm, "label"), Description: str(xm["description"]), Preview: strOf(xm, "previewMarkdown")}
		if r, ok := resolved[vi.ID]; ok {
			vi.Resolved, vi.ResolvedReason = str(r["verdict"]), str(r["reason"])
		}
		it.Items = append(it.Items, vi)
	}
	if it.Kind == "request_item_verdicts" && len(it.Verdicts) == 0 {
		it.Verdicts = []string{"approve", "reject"}
	}
	if b, ok := p["rejectRequiresReason"].(bool); ok {
		it.RejectRequiresReason = b
	}
	if it.Title == "" {
		it.Title = strOf(p, "title")
	}
	for _, q := range listFrom(p["questions"], "") {
		qm := obj(q)
		// required defaults to true: only an explicit false makes a question optional.
		qq := Question{ID: strOf(qm, "id"), Prompt: strOf(qm, "prompt"), HelpText: str(qm["helpText"]),
			SelectionMode: strOf(qm, "selectionMode"), Required: qm["required"] != false, Options: mapOptions(qm["options"])}
		if b, ok := qm["allowOther"].(bool); ok {
			qq.AllowOther = b
		}
		it.Questions = append(it.Questions, qq)
	}
	return it
}

// openIssues pages through the company's tasks (offset paging) and keeps
// the open ones; truncated reports that the page cap stopped the scan.
func (c *Connector) openIssues(ctx context.Context) (out []Issue, truncated bool, err error) {
	for page := 0; page < issuePageLimit; page++ {
		body, err := c.companyGet(ctx, fmt.Sprintf("/issues?limit=%d&offset=%d", issuePage, page*issuePage))
		if err != nil {
			return nil, false, err
		}
		batch := MapIssues(listFrom(body, "issues"))
		for _, is := range batch {
			if !closedStatuses[is.Status] && validID(is.ID) {
				out = append(out, is)
			}
		}
		if len(listFrom(body, "issues")) < issuePage {
			return out, false, nil
		}
	}
	return out, true, nil
}

// PendingInteractions lists every pending request across the company's open
// tasks, oldest first, read a few tasks at a time. Tasks whose requests could
// not be read are named in Warnings.
func (c *Connector) PendingInteractions(ctx context.Context) (*Inbox, error) {
	issues, truncated, err := c.openIssues(ctx)
	if err != nil {
		return nil, err
	}
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, 4)
		box = &Inbox{Items: []Interaction{}, Warnings: []string{}}
	)
	for _, is := range issues {
		wg.Add(1)
		go func(is Issue) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			body, err := c.get(ctx, "/api/issues/"+is.ID+"/interactions", issueTimeout)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				box.Warnings = append(box.Warnings, fmt.Sprintf("%s: requests could not be read (%v)", is.Identifier, err))
				return
			}
			for _, raw := range listFrom(body, "interactions") {
				if strOf(obj(raw), "status") == "pending" {
					box.Items = append(box.Items, MapInteraction(raw, is))
				}
			}
		}(is)
	}
	wg.Wait()
	sort.SliceStable(box.Items, func(i, j int) bool { return box.Items[i].CreatedAt < box.Items[j].CreatedAt })
	sort.Strings(box.Warnings)
	if truncated {
		box.Warnings = append(box.Warnings, fmt.Sprintf("Only the first %d tasks were checked; older tasks may also be waiting on you.", issuePage*issuePageLimit))
	}
	return box, nil
}

// IssueDocuments reads a task's documents (the work under review).
func (c *Connector) IssueDocuments(ctx context.Context, issueID string) ([]Document, error) {
	if !validID(issueID) {
		return nil, ErrBadID
	}
	body, err := c.get(ctx, "/api/issues/"+issueID+"/documents", issueTimeout)
	if err != nil {
		return nil, err
	}
	out := []Document{}
	for _, raw := range listFrom(body, "documents") {
		m := obj(raw)
		b := strOf(m, "body")
		if b == "" {
			b = strOf(obj(m["latestRevision"]), "body")
		}
		out = append(out, Document{Key: strOf(m, "key"), Title: strOf(m, "title"), Body: b, UpdatedAt: strOf(m, "updatedAt")})
	}
	return out, nil
}

// IssueThread reads a task's comments, oldest first, capped to the latest limit.
func (c *Connector) IssueThread(ctx context.Context, issueID string, limit int) ([]Comment, error) {
	if !validID(issueID) {
		return nil, ErrBadID
	}
	body, err := c.get(ctx, "/api/issues/"+issueID+"/comments", issueTimeout)
	if err != nil {
		return nil, err
	}
	cs := MapComments(listFrom(body, "comments"))
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].CreatedAt < cs[j].CreatedAt })
	if limit > 0 && len(cs) > limit {
		cs = cs[len(cs)-limit:]
	}
	return cs, nil
}

// AcceptInteraction approves a pending request (e.g. "Approve completion").
// choice carries the tasks to create (suggest_tasks) or the ticked boxes
// (request_checkbox_confirmation); empty for a plain review.
func (c *Connector) AcceptInteraction(ctx context.Context, issueID, interactionID string, choice AcceptChoice) error {
	if !validID(issueID) || !validID(interactionID) {
		return ErrBadID
	}
	body := map[string]any{}
	if len(choice.SelectedClientKeys) > 0 {
		body["selectedClientKeys"] = choice.SelectedClientKeys
	}
	if choice.SelectedOptionIDs != nil {
		body["selectedOptionIds"] = choice.SelectedOptionIDs
	}
	_, err := c.send(ctx, "paperclip.interaction.accept", http.MethodPost,
		"/api/issues/"+issueID+"/interactions/"+interactionID+"/accept", body)
	return err
}

// SubmitVerdicts records the operator's call on each item of a request_item_verdicts.
func (c *Connector) SubmitVerdicts(ctx context.Context, issueID, interactionID string, verdicts []Verdict) error {
	if !validID(issueID) || !validID(interactionID) {
		return ErrBadID
	}
	list := make([]map[string]any, 0, len(verdicts))
	for _, v := range verdicts {
		e := map[string]any{"id": v.ID, "verdict": v.Verdict}
		if v.Reason != nil && strings.TrimSpace(*v.Reason) != "" {
			e["reason"] = strings.TrimSpace(*v.Reason)
		}
		list = append(list, e)
	}
	_, err := c.send(ctx, "paperclip.interaction.verdicts", http.MethodPost,
		"/api/issues/"+issueID+"/interactions/"+interactionID+"/verdicts", map[string]any{"verdicts": list})
	return err
}

// RejectInteraction sends a request back ("Continue work"), with the reason.
func (c *Connector) RejectInteraction(ctx context.Context, issueID, interactionID, reason string) error {
	if !validID(issueID) || !validID(interactionID) {
		return ErrBadID
	}
	body := map[string]any{}
	if r := strings.TrimSpace(reason); r != "" {
		body["reason"] = r
	}
	_, err := c.send(ctx, "paperclip.interaction.reject", http.MethodPost,
		"/api/issues/"+issueID+"/interactions/"+interactionID+"/reject", body)
	return err
}

// RespondInteraction answers an agent's questions.
func (c *Connector) RespondInteraction(ctx context.Context, issueID, interactionID string, answers []Answer) error {
	if !validID(issueID) || !validID(interactionID) {
		return ErrBadID
	}
	list := make([]map[string]any, 0, len(answers))
	for _, a := range answers {
		e := map[string]any{"questionId": a.QuestionID, "optionIds": a.OptionIDs}
		if a.OtherText != nil {
			e["otherText"] = *a.OtherText
		}
		list = append(list, e)
	}
	_, err := c.send(ctx, "paperclip.interaction.respond", http.MethodPost,
		"/api/issues/"+issueID+"/interactions/"+interactionID+"/respond", map[string]any{"answers": list})
	return err
}

// CommentOnIssue posts the operator's instruction on any task (wakes its assignee).
func (c *Connector) CommentOnIssue(ctx context.Context, issueID, message string) (*Comment, error) {
	if !validID(issueID) {
		return nil, ErrBadID
	}
	body, err := c.send(ctx, "paperclip.issue.comment.create", http.MethodPost,
		"/api/issues/"+issueID+"/comments", map[string]any{"body": message})
	if err != nil {
		return nil, err
	}
	rec := obj(body)
	if inner, has := rec["comment"]; has {
		return first(MapComments([]any{inner})), nil
	}
	return first(MapComments([]any{body})), nil
}
