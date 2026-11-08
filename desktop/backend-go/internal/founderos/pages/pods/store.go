// Package pods is the client AI SDR pod (docs/tfg/CLIENT-PODS.md): a client
// workspace plus its sending identity, approval policy, limits, knowledge,
// approvals and activity. Status runs setup → dry_run → live ⇄ paused →
// handed_over; a pod only goes live after a dry run, with a sender and the
// client's approved targeting (icp) and voice.
package pods

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	StatusSetup      = "setup"
	StatusDryRun     = "dry_run"
	StatusLive       = "live"
	StatusPaused     = "paused"
	StatusHandedOver = "handed_over"
)

var (
	ErrExists     = errors.New("a pod for that client already exists")
	ErrNotFound   = errors.New("no such pod")
	ErrTransition = errors.New("that status change is not allowed")
	ErrNotReady   = errors.New("pod is not ready to go live")
)

// DocKeys are the pod documents agents work from.
var DocKeys = []string{"icp", "voice", "product", "objections", "targets", "exclusions", "playbook", "qualification", "handoff"}

// requiredToGoLive are the documents the client must have approved.
var requiredToGoLive = []string{"icp", "voice"}

var allowed = map[string][]string{
	StatusSetup:      {StatusDryRun},
	StatusDryRun:     {StatusSetup, StatusLive, StatusPaused},
	StatusLive:       {StatusPaused, StatusHandedOver},
	StatusPaused:     {StatusLive, StatusDryRun, StatusHandedOver},
	StatusHandedOver: {},
}

type Pod struct {
	ID, WorkspaceID, Slug, ClientName string
	PortalClientID                    *string
	Package                           string
	Reps                              int
	Status                            string
	SenderName, SenderEmail           string
	SendingAccountID                  *string
	SendingDomain, DryRunInbox        string
	ApprovalPolicy                    string
	FirstEmailDailyCap                int
	FollowUpDailyCap                  int
	Timezone                          string
	SendWindowStart, SendWindowEnd    int
	SendWeekends                      bool
	CRMProvider, CalendarProvider     string
	BookingURL                        string
	PausedReason                      string
	CreatedAt, UpdatedAt              time.Time
	WentLiveAt                        *time.Time
}

type NewPod struct {
	ClientName, OwnerID, Package string
	Reps                         int
}

type Sender struct{ Name, Email, Domain, AccountID string }

type Doc struct {
	Key, Body  string
	Version    int
	ApprovedAt *time.Time
	UpdatedAt  time.Time
}

type Activity struct {
	At                 time.Time
	Kind, Summary, Ref string
}

type Store struct{ Pool *pgxpool.Pool }

const podCols = `id::text, workspace_id::text, slug, client_name, portal_client_id, package, reps, status,
	sender_name, sender_email, sending_account_id, sending_domain, dry_run_inbox, approval_policy,
	first_email_daily_cap, follow_up_daily_cap, timezone, send_window_start, send_window_end,
	send_weekends, crm_provider, calendar_provider, booking_url, paused_reason, created_at,
	updated_at, went_live_at`

func scanPod(row pgx.Row) (Pod, error) {
	var p Pod
	var ws, we int16
	err := row.Scan(&p.ID, &p.WorkspaceID, &p.Slug, &p.ClientName, &p.PortalClientID, &p.Package, &p.Reps,
		&p.Status, &p.SenderName, &p.SenderEmail, &p.SendingAccountID, &p.SendingDomain, &p.DryRunInbox,
		&p.ApprovalPolicy, &p.FirstEmailDailyCap, &p.FollowUpDailyCap, &p.Timezone, &ws, &we,
		&p.SendWeekends, &p.CRMProvider, &p.CalendarProvider, &p.BookingURL, &p.PausedReason,
		&p.CreatedAt, &p.UpdatedAt, &p.WentLiveAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	p.SendWindowStart, p.SendWindowEnd = int(ws), int(we)
	return p, err
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// SlugFor turns a client name into its pod slug.
func SlugFor(name string) string {
	s := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(s) > 60 {
		s = strings.TrimRight(s[:60], "-")
	}
	return "client-" + s
}

// Create makes the client's workspace and pod together.
func (s *Store) Create(ctx context.Context, n NewPod) (Pod, error) {
	name := strings.Join(strings.Fields(n.ClientName), " ")
	if name == "" || SlugFor(name) == "client-" {
		return Pod{}, errors.New("client name is required")
	}
	if n.OwnerID == "" {
		return Pod{}, errors.New("owner is required")
	}
	pkg := n.Package
	if pkg == "" {
		pkg = "custom"
	}
	reps := n.Reps
	if reps <= 0 {
		reps = 1
	}
	slug := SlugFor(name)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Pod{}, err
	}
	defer tx.Rollback(ctx)
	var ws string
	err = tx.QueryRow(ctx, `INSERT INTO workspaces (name, slug, owner_id, description)
		VALUES ($1, $2, $3, 'Client AI SDR pod') RETURNING id::text`, name, slug, n.OwnerID).Scan(&ws)
	if err != nil {
		return Pod{}, uniqueOr(err)
	}
	p, err := scanPod(tx.QueryRow(ctx, `INSERT INTO founderos_client_pods (workspace_id, slug, client_name, package, reps)
		VALUES ($1, $2, $3, $4, $5) RETURNING `+podCols, ws, slug, name, pkg, reps))
	if err != nil {
		return Pod{}, uniqueOr(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO founderos_client_activity (pod_id, kind, summary) VALUES ($1, 'pod_created', $2)`,
		p.ID, "Pod created for "+name); err != nil {
		return Pod{}, err
	}
	return p, tx.Commit(ctx)
}

func uniqueOr(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return ErrExists
	}
	return err
}

func (s *Store) Get(ctx context.Context, slug string) (Pod, error) {
	return scanPod(s.Pool.QueryRow(ctx, `SELECT `+podCols+` FROM founderos_client_pods WHERE slug = $1`, slug))
}

func (s *Store) List(ctx context.Context) ([]Pod, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+podCols+` FROM founderos_client_pods ORDER BY client_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Pod
	for rows.Next() {
		p, err := scanPod(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetSender records who the pod's emails come from.
func (s *Store) SetSender(ctx context.Context, slug string, snd Sender) error {
	email := strings.TrimSpace(strings.ToLower(snd.Email))
	if !strings.Contains(email, "@") || strings.ContainsAny(email, " <>") {
		return fmt.Errorf("sender email %q is not valid", snd.Email)
	}
	domain := strings.TrimSpace(strings.ToLower(snd.Domain))
	if domain == "" {
		domain = email[strings.LastIndex(email, "@")+1:]
	}
	var acct any
	if snd.AccountID != "" {
		acct = snd.AccountID
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE founderos_client_pods SET sender_name = $2, sender_email = $3,
		sending_domain = $4, sending_account_id = $5, updated_at = now() WHERE slug = $1`,
		slug, strings.TrimSpace(snd.Name), email, domain, acct)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func validKey(k string) bool {
	for _, d := range DocKeys {
		if d == k {
			return true
		}
	}
	return false
}

// SetDoc saves a pod document; a changed body is a new version that needs approval again.
func (s *Store) SetDoc(ctx context.Context, slug, key, body string) error {
	if !validKey(key) {
		return fmt.Errorf("unknown document %q", key)
	}
	tag, err := s.Pool.Exec(ctx, `INSERT INTO founderos_client_pod_docs (pod_id, key, body)
		SELECT id, $2, $3 FROM founderos_client_pods WHERE slug = $1
		ON CONFLICT (pod_id, key) DO UPDATE SET
			version = founderos_client_pod_docs.version + 1, body = EXCLUDED.body,
			approved_at = NULL, updated_at = now()
		WHERE founderos_client_pod_docs.body IS DISTINCT FROM EXCLUDED.body`, slug, key, body)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		if _, err := s.Get(ctx, slug); err != nil {
			return err
		}
	}
	return nil
}

// ApproveDoc records the client's sign-off on the current version.
func (s *Store) ApproveDoc(ctx context.Context, slug, key, by string) error {
	var podID string
	err := s.Pool.QueryRow(ctx, `UPDATE founderos_client_pod_docs d SET approved_at = now()
		FROM founderos_client_pods p WHERE p.id = d.pod_id AND p.slug = $1 AND d.key = $2
		RETURNING p.id::text`, slug, key).Scan(&podID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return s.Log(ctx, podID, "doc_approved", fmt.Sprintf("%s approved by %s", key, by), key)
}

func (s *Store) Doc(ctx context.Context, slug, key string) (Doc, error) {
	var d Doc
	err := s.Pool.QueryRow(ctx, `SELECT d.key, d.body, d.version, d.approved_at, d.updated_at
		FROM founderos_client_pod_docs d JOIN founderos_client_pods p ON p.id = d.pod_id
		WHERE p.slug = $1 AND d.key = $2`, slug, key).Scan(&d.Key, &d.Body, &d.Version, &d.ApprovedAt, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// Readiness lists what stops the pod going live (empty = ready).
func (s *Store) Readiness(ctx context.Context, p Pod) ([]string, error) {
	var missing []string
	if p.SenderEmail == "" {
		missing = append(missing, "a sender email")
	}
	for _, k := range requiredToGoLive {
		d, err := s.Doc(ctx, p.Slug, k)
		switch {
		case errors.Is(err, ErrNotFound) || (err == nil && strings.TrimSpace(d.Body) == ""):
			missing = append(missing, "the "+k+" document")
		case err != nil:
			return nil, err
		case d.ApprovedAt == nil:
			missing = append(missing, "client approval of the "+k+" document")
		}
	}
	return missing, nil
}

// SetStatus moves the pod through its lifecycle. Pausing needs a reason.
func (s *Store) SetStatus(ctx context.Context, slug, to, reason string) (Pod, error) {
	p, err := s.Get(ctx, slug)
	if err != nil {
		return p, err
	}
	ok := false
	for _, n := range allowed[p.Status] {
		ok = ok || n == to
	}
	if !ok {
		return p, fmt.Errorf("%w: %s → %s", ErrTransition, p.Status, to)
	}
	if to == StatusPaused && strings.TrimSpace(reason) == "" {
		return p, errors.New("pausing needs a reason")
	}
	if to == StatusLive {
		missing, err := s.Readiness(ctx, p)
		if err != nil {
			return p, err
		}
		if len(missing) > 0 {
			return p, fmt.Errorf("%w: needs %s", ErrNotReady, strings.Join(missing, ", "))
		}
	}
	if to != StatusPaused {
		reason = ""
	}
	p, err = scanPod(s.Pool.QueryRow(ctx, `UPDATE founderos_client_pods SET status = $2, paused_reason = $3,
		went_live_at = CASE WHEN $2 = 'live' THEN coalesce(went_live_at, now()) ELSE went_live_at END,
		updated_at = now() WHERE slug = $1 RETURNING `+podCols, slug, to, strings.TrimSpace(reason)))
	if err != nil {
		return p, err
	}
	summary := "Status: " + strings.ReplaceAll(to, "_", " ")
	if to == StatusPaused {
		summary += " (" + p.PausedReason + ")"
	}
	return p, s.Log(ctx, p.ID, "status", summary, to)
}

// Log adds one activity line.
func (s *Store) Log(ctx context.Context, podID, kind, summary, ref string) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO founderos_client_activity (pod_id, kind, summary, ref) VALUES ($1, $2, $3, $4)`,
		podID, kind, summary, ref)
	return err
}

func (s *Store) Recent(ctx context.Context, podID string, limit int) ([]Activity, error) {
	rows, err := s.Pool.Query(ctx, `SELECT at, kind, summary, ref FROM founderos_client_activity
		WHERE pod_id = $1 ORDER BY at DESC, id DESC LIMIT $2`, podID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Activity
	for rows.Next() {
		var a Activity
		if err := rows.Scan(&a.At, &a.Kind, &a.Summary, &a.Ref); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
