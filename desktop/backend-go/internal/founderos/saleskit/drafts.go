package saleskit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Draft struct {
	ContactID string `json:"contactId"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
}
type Analysis struct {
	Report string  `json:"report"`
	Drafts []Draft `json:"drafts"`
}

const AnalysisFormat = `
Return a JSON object (no code fence) with "report" containing the consolidated Markdown report, and "drafts" containing zero or more objects {"contactId":"exact supplied OS contact id","subject":"...","body":"..."}.
Only draft first-touch email for changed, qualified prospects supported by the approved profile and supplied evidence. If this is a review or audit rather than an outreach request, use an empty drafts array. Do not draft for contacts who replied, booked, opted out or were already contacted. Never guess recipient addresses. The OS resolves contact IDs against authorised pipeline records, suppresses existing outreach and puts valid drafts in Outbox for owner approval. Do not claim a message was sent. This text-only runner cannot create task documents or perform external actions.
`

func ParseAnalysis(raw string) Analysis {
	trim := strings.TrimSpace(raw)
	trim = strings.TrimPrefix(trim, "```json")
	trim = strings.TrimPrefix(trim, "```")
	trim = strings.TrimSuffix(trim, "```")
	var a Analysis
	if json.Unmarshal([]byte(strings.TrimSpace(trim)), &a) != nil || strings.TrimSpace(a.Report) == "" {
		return Analysis{Report: raw, Drafts: []Draft{}}
	}
	if len(a.Drafts) > 30 {
		a.Drafts = a.Drafts[:30]
	}
	return a
}

// QueueDrafts resolves addresses from existing records, never from model output.
// It performs no external writes and cannot bypass the Outbox approval route.
func QueueDrafts(ctx context.Context, pool *pgxpool.Pool, c AutomationConfig, ref string, drafts []Draft) (int, error) {
	if pool == nil {
		return 0, errors.New("outbox storage unavailable")
	}
	ids := append([]string{c.WorkspaceID}, c.SourceWorkspaceIDs...)
	n := 0
	for _, d := range drafts {
		d.Subject = strings.TrimSpace(d.Subject)
		d.Body = strings.TrimSpace(d.Body)
		if d.ContactID == "" || len(d.Subject) == 0 || len([]rune(d.Subject)) > 300 || strings.ContainsAny(d.Subject, "\r\n") || len(d.Body) == 0 || len([]rune(d.Body)) > 20000 {
			continue
		}
		// A transaction-scoped contact lock serialises drafts from commands and
		// scheduled analyses. Existing sent/rejected drafts are not recreated.
		tx, err := pool.Begin(ctx)
		if err != nil {
			return n, err
		}
		_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, d.ContactID)
		if err != nil {
			tx.Rollback(ctx)
			return n, err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO founderos_outbox(id,workspace_id,source,source_ref,contact_id,to_address,to_name,company,subject,body)
 SELECT $1,hq.id,'saleskit',$2,c.id,c.email,c.person,c.company,$4,$5
 FROM founderos_funnel_contacts c JOIN workspaces hq ON hq.slug='founderos'
 WHERE c.id=$3 AND c.workspace_id=ANY($6::uuid[]) AND c.status IN ('first_touch','nurtured')
 AND c.email ~ '^[^[:space:]@]+@[^[:space:]@]+\.[^[:space:]@]+$'
 AND lower(c.email) NOT LIKE '%@thefoundersgroup.net'
 AND NOT EXISTS(SELECT 1 FROM founderos_outbox o WHERE o.contact_id=c.id OR lower(o.to_address)=lower(c.email))
 ON CONFLICT(id) DO NOTHING`, "sk-"+Digest(d.ContactID)[:24], ref, d.ContactID, d.Subject, d.Body, ids)
		if err != nil {
			tx.Rollback(ctx)
			return n, err
		}
		if err = tx.Commit(ctx); err != nil {
			return n, err
		}
		n += int(tag.RowsAffected())
	}
	return n, nil
}
