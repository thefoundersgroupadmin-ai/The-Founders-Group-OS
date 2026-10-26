package saleskit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type AutomationConfig struct {
	WorkspaceID string     `json:"workspaceId"`
	Workspace   string     `json:"workspace"`
	Name        string     `json:"name"`
	UserID      string     `json:"-"`
	UserEmail   string     `json:"-"`
	UserName    string     `json:"-"`
	Profile     string     `json:"profile"`
	Enabled     bool       `json:"enabled"`
	Timezone    string     `json:"timezone"`
	Checkpoint  Checkpoint `json:"-"`
}
type AutomationRun struct {
	ID          string     `json:"id"`
	WorkspaceID string     `json:"workspaceId"`
	Routines    []string   `json:"routines"`
	Skills      []string   `json:"skills"`
	SourceIDs   []string   `json:"sourceIds"`
	Status      string     `json:"status"`
	Markdown    string     `json:"markdown"`
	Error       string     `json:"error"`
	StartedAt   time.Time  `json:"startedAt"`
	CompletedAt *time.Time `json:"completedAt"`
}
type AutomationStore struct{ Pool *pgxpool.Pool }

func (s AutomationStore) Workspaces(ctx context.Context, userID string, owner bool) ([]AutomationConfig, error) {
	if s.Pool == nil {
		return nil, errors.New("sales automation needs the OS database")
	}
	rows, err := s.Pool.Query(ctx, `SELECT w.id::text,w.slug,w.name,COALESCE(a.user_id,''),COALESCE(a.user_email,''),COALESCE(a.user_name,''),COALESCE(a.profile,''),COALESCE(a.enabled,false),COALESCE(a.timezone,'Europe/London'),COALESCE(a.checkpoint,'{}'::jsonb)
 FROM workspaces w LEFT JOIN founderos_sales_automation a ON a.workspace_id=w.id
 WHERE ($2 OR w.owner_id=$1 OR EXISTS(SELECT 1 FROM workspace_members m WHERE m.workspace_id=w.id AND m.user_id=$1 AND m.status='active'))
 ORDER BY w.name`, userID, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AutomationConfig{}
	for rows.Next() {
		var c AutomationConfig
		var raw []byte
		if err := rows.Scan(&c.WorkspaceID, &c.Workspace, &c.Name, &c.UserID, &c.UserEmail, &c.UserName, &c.Profile, &c.Enabled, &c.Timezone, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &c.Checkpoint); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (s AutomationStore) Save(ctx context.Context, c AutomationConfig) error {
	if strings.TrimSpace(c.Profile) == "" || len(c.Profile) > 24000 {
		return errors.New("configure the client sales profile once before enabling automation")
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return errors.New("invalid timezone")
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO founderos_sales_automation(workspace_id,user_id,user_email,user_name,profile,enabled,timezone) VALUES($1::uuid,$2,$3,$4,$5,$6,$7)
 ON CONFLICT(workspace_id) DO UPDATE SET user_id=EXCLUDED.user_id,user_email=EXCLUDED.user_email,user_name=EXCLUDED.user_name,
 checkpoint=CASE WHEN founderos_sales_automation.profile IS DISTINCT FROM EXCLUDED.profile THEN '{}'::jsonb ELSE founderos_sales_automation.checkpoint END,
 profile=EXCLUDED.profile,enabled=EXCLUDED.enabled,timezone=EXCLUDED.timezone,updated_at=now()`, c.WorkspaceID, c.UserID, c.UserEmail, c.UserName, c.Profile, c.Enabled, c.Timezone)
	return err
}

func (s AutomationStore) Snapshot(ctx context.Context, workspaceID string) (Snapshot, error) {
	snap := Snapshot{Sources: []Source{}, Gaps: []string{"Email, calendar availability, contracts and approved knowledge documents are not yet connected to this automation. Their workflows are not scheduled."}}
	// Workspaces are constrained on BOTH sides of the activity join.
	leads, err := s.Pool.Query(ctx, `SELECT to_jsonb(c)||jsonb_build_object('touches',COALESCE((SELECT jsonb_agg(to_jsonb(t) ORDER BY t.at,t.seq,t.id) FROM founderos_funnel_touches t WHERE t.contact_id=c.id AND t.workspace_id=c.workspace_id),'[]'::jsonb))
 FROM founderos_funnel_contacts c WHERE c.workspace_id=$1::uuid ORDER BY c.imported_at DESC,c.id LIMIT 201`, workspaceID)
	if err != nil {
		return snap, err
	}
	leadData := []json.RawMessage{}
	for leads.Next() {
		var b []byte
		if err := leads.Scan(&b); err != nil {
			leads.Close()
			return snap, err
		}
		leadData = append(leadData, append([]byte(nil), b...))
	}
	err = leads.Err()
	leads.Close()
	if err != nil {
		return snap, err
	}
	calls, err := s.Pool.Query(ctx, `SELECT jsonb_build_object('source',source,'external_id',external_id,'entry',entry,'archived_at',archived_at)
 FROM founderos_call_archive WHERE workspace_id=$1::uuid ORDER BY archived_at DESC,source,external_id LIMIT 21`, workspaceID)
	if err != nil {
		return snap, err
	}
	callData := []json.RawMessage{}
	for calls.Next() {
		var b []byte
		if err := calls.Scan(&b); err != nil {
			calls.Close()
			return snap, err
		}
		callData = append(callData, append([]byte(nil), b...))
	}
	err = calls.Err()
	calls.Close()
	if err != nil {
		return snap, err
	}
	appendSource := func(id, title string, data []json.RawMessage, limit, maxBytes int) {
		if len(data) == 0 {
			return
		}
		if len(data) > limit {
			data = data[:limit]
			snap.Gaps = append(snap.Gaps, fmt.Sprintf("%s limited to the %d most recently synced records; this is not the complete workspace history", title, limit))
		}
		b, _ := json.Marshal(data)
		text := string(b)
		if len(text) > maxBytes {
			text = strings.ToValidUTF8(text[:maxBytes], "") + "\n[EXCERPT: remaining data omitted]"
			snap.Gaps = append(snap.Gaps, title+" is a size-limited excerpt")
		}
		snap.Sources = append(snap.Sources, Source{ID: id, Title: title, Text: text})
	}
	snap.HasLeads = len(leadData) > 0
	snap.HasCalls = len(callData) > 0
	snap.LeadRecords = recordRevisions(leadData[:min(len(leadData), 200)], false)
	snap.CallRecords = recordRevisions(callData[:min(len(callData), 20)], true)
	snap.LeadRevision = Digest(snap.LeadRecords)
	snap.CallRevision = Digest(snap.CallRecords)
	appendSource("os-pipeline", "Synced OS pipeline records", leadData, 200, 60000)
	appendSource("os-calls", "Archived OS call records", callData, 20, 50000)
	if !snap.HasLeads {
		snap.Gaps = append(snap.Gaps, "No synced pipeline records in this workspace")
	}
	if !snap.HasCalls {
		snap.Gaps = append(snap.Gaps, "No archived calls in this workspace")
	}
	return snap, nil
}
func (s AutomationStore) Claim(ctx context.Context, c AutomationConfig, p AutomationPlan, snap Snapshot) (string, bool, error) {
	routines, _ := json.Marshal(p.Routines)
	skills, _ := json.Marshal(p.Skills)
	ids := []string{}
	for _, src := range snap.Sources {
		ids = append(ids, src.ID)
	}
	sources, _ := json.Marshal(ids)
	var id string
	err := s.Pool.QueryRow(ctx, `INSERT INTO founderos_sales_automation_runs(workspace_id,batch_key,routines,skills,source_ids,status)
 VALUES($1::uuid,$2,$3,$4,$5,'running') ON CONFLICT(workspace_id,batch_key) DO NOTHING RETURNING id::text`, c.WorkspaceID, BatchKey(p, snap, c.Profile), routines, skills, sources).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return id, err == nil, err
}
func (s AutomationStore) Finish(ctx context.Context, c AutomationConfig, id string, next Checkpoint, answer string, runErr error) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	status, msg := "complete", ""
	if runErr != nil {
		status, msg = "error", runErr.Error()
	}
	if _, err = tx.Exec(ctx, `UPDATE founderos_sales_automation_runs SET status=$2,markdown=$3,error=$4,completed_at=now() WHERE id=$1::uuid`, id, status, answer, msg); err != nil {
		return err
	}
	if runErr == nil {
		raw, _ := json.Marshal(next)
		// An operator may have edited or disabled the config while a run was in flight.
		_, err = tx.Exec(ctx, `UPDATE founderos_sales_automation SET checkpoint=$2 WHERE workspace_id=$1::uuid AND profile=$3 AND enabled=true`, c.WorkspaceID, raw, c.Profile)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func (s AutomationStore) Runs(ctx context.Context, workspaceID string) ([]AutomationRun, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id::text,workspace_id::text,routines,skills,source_ids,status,markdown,error,started_at,completed_at
 FROM founderos_sales_automation_runs WHERE workspace_id=$1::uuid ORDER BY started_at DESC LIMIT 20`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AutomationRun{}
	for rows.Next() {
		var r AutomationRun
		var a, b, c []byte
		if err := rows.Scan(&r.ID, &r.WorkspaceID, &a, &b, &c, &r.Status, &r.Markdown, &r.Error, &r.StartedAt, &r.CompletedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(a, &r.Routines); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &r.Skills); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(c, &r.SourceIDs); err != nil {
			return nil, err
		}
		if r.Status == "running" && time.Since(r.StartedAt) > 10*time.Minute {
			r.Status = "interrupted"
			r.Error = "Run did not complete. It will not be automatically repeated for the same source packet."
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Source refresh timestamps alone must not create new outreach work.
func recordRevisions(records []json.RawMessage, calls bool) map[string]string {
	out := map[string]string{}
	for _, raw := range records {
		var record map[string]any
		if json.Unmarshal(raw, &record) != nil {
			continue
		}
		id, _ := record["id"].(string)
		if calls {
			source, _ := record["source"].(string)
			external, _ := record["external_id"].(string)
			id = source + ":" + external
		}
		if id != "" {
			out[id] = Digest(withoutRefreshTimes(record))
		}
	}
	return out
}
func withoutRefreshTimes(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, item := range v {
			if k != "imported_at" && k != "archived_at" {
				out[k] = withoutRefreshTimes(item)
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = withoutRefreshTimes(item)
		}
		return out
	default:
		return value
	}
}
