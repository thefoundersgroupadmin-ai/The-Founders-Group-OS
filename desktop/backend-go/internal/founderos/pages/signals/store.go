package signals

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// openStatuses are the pipeline stages still worth a signal check.
var openStatuses = []string{"first_touch", "engaged", "nurtured"}

// Due lists open prospects whose sources were never read or were read longer
// than staleAfter ago, never-read first.
func Due(ctx context.Context, pool *pgxpool.Pool, wsID string, staleAfter time.Duration, limit int) ([]Prospect, error) {
	rows, err := pool.Query(ctx, `SELECT c.id, COALESCE(NULLIF(c.company, ''), c.name), COALESCE(c.website, ''), COALESCE(c.research_evidence, ''), c.status
		FROM founderos_funnel_contacts c
		LEFT JOIN founderos_signal_checks k ON k.workspace_id = c.workspace_id AND k.contact_id = c.id
		WHERE c.workspace_id = $1::uuid AND c.status = ANY($2)
		  AND (k.checked_at IS NULL OR k.checked_at < now() - make_interval(secs => $3))
		ORDER BY k.checked_at NULLS FIRST, c.likelihood DESC, c.id
		LIMIT $4`, wsID, openStatuses, staleAfter.Seconds(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Prospect
	for rows.Next() {
		var p Prospect
		if err := rows.Scan(&p.ID, &p.Company, &p.Website, &p.Evidence, &p.Status); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// One reads a single prospect for an on-demand check.
func One(ctx context.Context, pool *pgxpool.Pool, wsID, contactID string) (Prospect, error) {
	var p Prospect
	err := pool.QueryRow(ctx, `SELECT id, COALESCE(NULLIF(company, ''), name), COALESCE(website, ''), COALESCE(research_evidence, ''), status
		FROM founderos_funnel_contacts WHERE workspace_id = $1::uuid AND id = $2`, wsID, contactID).
		Scan(&p.ID, &p.Company, &p.Website, &p.Evidence, &p.Status)
	return p, err
}

// Save replaces a prospect's signals with this check's (a closed role or a
// removed tool disappears; a fact seen again keeps its first-seen date),
// records the check, and fills the contact's LinkedIn from the research.
func Save(ctx context.Context, pool *pgxpool.Pool, wsID string, p Prospect, r Result) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	keep := []string{}
	for _, s := range r.Signals {
		keep = append(keep, s.Fingerprint)
		var date any
		if s.Date != nil {
			date = s.Date.Format("2006-01-02")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO founderos_signals (workspace_id, contact_id, kind, title, detail, url, weight, signal_date, fingerprint)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8::date, $9)
			ON CONFLICT (workspace_id, contact_id, fingerprint) DO UPDATE SET title = EXCLUDED.title, detail = EXCLUDED.detail,
			  url = EXCLUDED.url, weight = EXCLUDED.weight, signal_date = EXCLUDED.signal_date, last_seen = now()`,
			wsID, p.ID, s.Kind, clip(s.Title, 300), clip(s.Detail, 500), clip(s.URL, 1000), s.Weight, date, s.Fingerprint); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM founderos_signals WHERE workspace_id = $1::uuid AND contact_id = $2 AND NOT (fingerprint = ANY($3))`,
		wsID, p.ID, keep); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO founderos_signal_checks (workspace_id, contact_id, checked_at, sources, problem)
		VALUES ($1::uuid, $2, now(), $3, $4)
		ON CONFLICT (workspace_id, contact_id) DO UPDATE SET checked_at = now(), sources = EXCLUDED.sources, problem = EXCLUDED.problem`,
		wsID, p.ID, strings.Join(r.Sources, ","), clip(strings.Join(r.Problems, "; "), 500)); err != nil {
		return err
	}
	if li := LinkedIn(p.Evidence); li != "" {
		if _, err := tx.Exec(ctx, `UPDATE founderos_funnel_contacts SET linkedin = $3
			WHERE workspace_id = $1::uuid AND id = $2 AND COALESCE(linkedin, '') = ''`, wsID, p.ID, li); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Row is one prospect on the signals board.
type Row struct {
	ContactID string   `json:"contactId"`
	Person    string   `json:"person"`
	Company   string   `json:"company"`
	Role      string   `json:"role"`
	Status    string   `json:"status"`
	Website   string   `json:"website"`
	Email     string   `json:"email"`
	Phone     string   `json:"phone"`
	LinkedIn  string   `json:"linkedin"`
	Score     int      `json:"score"`
	WhyNow    string   `json:"whyNow"`
	Signals   []Signal `json:"signals"`
	CheckedAt *string  `json:"checkedAt"`
	Problem   string   `json:"problem,omitempty"`
	Plays     Plays    `json:"plays"`
}

// Stats is the board's top line.
type Stats struct {
	Open, Checked, WithSignals, Hot, Hiring, Funded, NoEmail int
}

// Board ranks every open prospect by signal score, strongest first.
func Board(ctx context.Context, pool *pgxpool.Pool, wsID string, limit int) ([]Row, Stats, error) {
	var st Stats
	rows, err := pool.Query(ctx, `SELECT c.id, COALESCE(c.person, ''), COALESCE(NULLIF(c.company, ''), c.name), COALESCE(NULLIF(c.buyer_role, ''), c.role, ''),
			c.status, COALESCE(c.website, ''), COALESCE(c.email, ''), COALESCE(c.phone, ''), COALESCE(c.linkedin, ''),
			to_char(k.checked_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), COALESCE(k.problem, '')
		FROM founderos_funnel_contacts c
		LEFT JOIN founderos_signal_checks k ON k.workspace_id = c.workspace_id AND k.contact_id = c.id
		WHERE c.workspace_id = $1::uuid AND c.status = ANY($2)`, wsID, openStatuses)
	if err != nil {
		return nil, st, err
	}
	byID := map[string]*Row{}
	var list []*Row
	for rows.Next() {
		r := &Row{}
		if err := rows.Scan(&r.ContactID, &r.Person, &r.Company, &r.Role, &r.Status, &r.Website, &r.Email, &r.Phone, &r.LinkedIn, &r.CheckedAt, &r.Problem); err != nil {
			rows.Close()
			return nil, st, err
		}
		r.Signals = []Signal{}
		byID[r.ContactID] = r
		list = append(list, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, st, err
	}
	sr, err := pool.Query(ctx, `SELECT contact_id, kind, title, detail, url, weight, signal_date
		FROM founderos_signals WHERE workspace_id = $1::uuid ORDER BY weight DESC, id`, wsID)
	if err != nil {
		return nil, st, err
	}
	for sr.Next() {
		var id string
		var s Signal
		var d *time.Time
		if err := sr.Scan(&id, &s.Kind, &s.Title, &s.Detail, &s.URL, &s.Weight, &d); err != nil {
			sr.Close()
			return nil, st, err
		}
		s.Date = d
		if r := byID[id]; r != nil {
			r.Signals = append(r.Signals, s)
		}
	}
	sr.Close()
	if err := sr.Err(); err != nil {
		return nil, st, err
	}
	for _, r := range list {
		r.Score = Score(r.Signals)
		r.WhyNow = WhyNow(r.Signals)
		r.Plays = PlaysFor(*r)
		st.Open++
		if r.CheckedAt != nil {
			st.Checked++
		}
		if r.Score > 0 {
			st.WithSignals++
		}
		if r.Score >= 40 {
			st.Hot++
		}
		if r.Email == "" {
			st.NoEmail++
		}
		for _, s := range r.Signals {
			if s.Kind == "hiring" && s.Weight >= 20 {
				st.Hiring++
				break
			}
		}
		for _, s := range r.Signals {
			if s.Kind == "funding" && s.Weight >= 25 {
				st.Funded++
				break
			}
		}
	}
	sort.SliceStable(list, func(a, b int) bool {
		if list[a].Score != list[b].Score {
			return list[a].Score > list[b].Score
		}
		return list[a].Company < list[b].Company
	})
	if limit > 0 && len(list) > limit {
		list = list[:limit]
	}
	out := make([]Row, len(list))
	for i, r := range list {
		out[i] = *r
	}
	return out, st, nil
}
