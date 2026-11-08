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
	ErrInvalid    = errors.New("invalid")
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
	ConfigChangedAt                   time.Time
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
	updated_at, went_live_at, config_changed_at`

func scanPod(row pgx.Row) (Pod, error) {
	var p Pod
	var ws, we int16
	err := row.Scan(&p.ID, &p.WorkspaceID, &p.Slug, &p.ClientName, &p.PortalClientID, &p.Package, &p.Reps,
		&p.Status, &p.SenderName, &p.SenderEmail, &p.SendingAccountID, &p.SendingDomain, &p.DryRunInbox,
		&p.ApprovalPolicy, &p.FirstEmailDailyCap, &p.FollowUpDailyCap, &p.Timezone, &ws, &we,
		&p.SendWeekends, &p.CRMProvider, &p.CalendarProvider, &p.BookingURL, &p.PausedReason,
		&p.CreatedAt, &p.UpdatedAt, &p.WentLiveAt, &p.ConfigChangedAt)
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
		return Pod{}, fmt.Errorf("%w: client name is required", ErrInvalid)
	}
	if n.OwnerID == "" {
		return Pod{}, fmt.Errorf("%w: owner is required", ErrInvalid)
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

// lockedExec runs one write under the pod send gate's lock (outbox/podgate.go),
// so it lands between sends, never in the middle of one.
func (s *Store) lockedExec(ctx context.Context, slug, sql string, args ...any) (int64, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('founderos-pod-send:' || workspace_id::text))
		FROM founderos_client_pods WHERE slug = $1`, slug); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, sql, args...)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), tx.Commit(ctx)
}

// SetSender records who the pod's emails come from.
func (s *Store) SetSender(ctx context.Context, slug string, snd Sender) error {
	email := strings.TrimSpace(strings.ToLower(snd.Email))
	if !strings.Contains(email, "@") || strings.ContainsAny(email, " <>") {
		return fmt.Errorf("%w: sender email %q is not valid", ErrInvalid, snd.Email)
	}
	domain := strings.TrimSpace(strings.ToLower(snd.Domain))
	if domain == "" {
		domain = email[strings.LastIndex(email, "@")+1:]
	}
	var acct any
	if snd.AccountID != "" {
		// Codex M1 review #2: only the pod's own mailbox, never another pod's or HQ's.
		if snd.AccountID != "pod:"+slug {
			return fmt.Errorf("%w: a pod sends only from its own mailbox (pod:%s)", ErrInvalid, slug)
		}
		acct = snd.AccountID
	}
	n, err := s.lockedExec(ctx, slug, `UPDATE founderos_client_pods SET sender_name = $2, sender_email = $3,
		sending_domain = $4, sending_account_id = $5, updated_at = now(), config_changed_at = now() WHERE slug = $1`,
		slug, strings.TrimSpace(snd.Name), email, domain, acct)
	if err == nil && n == 0 {
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
		return fmt.Errorf("%w: unknown document %q", ErrInvalid, key)
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
		return nil
	}
	// The client approves each version of a document in the portal.
	return s.fileDocApproval(ctx, slug, key, body)
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
	if p.SendingAccountID == nil || *p.SendingAccountID != "pod:"+p.Slug {
		missing = append(missing, "the client's own connected sending mailbox")
	}
	var simulated bool
	if err := s.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM founderos_outbox WHERE workspace_id = $1
		AND status = 'simulated' AND sent_at > $2)`, p.WorkspaceID, p.ConfigChangedAt).Scan(&simulated); err != nil {
		return nil, err
	}
	if !simulated {
		missing = append(missing, "a successful test run since the last change to the sender or sending rules")
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

// SetStatus moves the pod through its lifecycle. Pausing needs a reason. The
// move and its readiness check happen under the pod send gate's lock, so a
// status change waits for an email being handed to the mail server, every
// later send sees it, and nothing changes between the check and the move.
func (s *Store) SetStatus(ctx context.Context, slug, to, reason string) (Pod, error) {
	p, err := s.Get(ctx, slug)
	if err != nil {
		return p, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return p, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('founderos-pod-send:' || $1))`, p.WorkspaceID); err != nil {
		return p, err
	}
	if p, err = scanPod(tx.QueryRow(ctx, `SELECT `+podCols+` FROM founderos_client_pods WHERE slug = $1`, slug)); err != nil {
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
		return p, fmt.Errorf("%w: pausing needs a reason", ErrInvalid)
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
	p, err = scanPod(tx.QueryRow(ctx, `UPDATE founderos_client_pods SET status = $2, paused_reason = $3,
		went_live_at = CASE WHEN $2 = 'live' THEN coalesce(went_live_at, now()) ELSE went_live_at END,
		updated_at = now() WHERE slug = $1 RETURNING `+podCols, slug, to, strings.TrimSpace(reason)))
	if err != nil {
		return p, err
	}
	if err := tx.Commit(ctx); err != nil {
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

// Viewer is who is asking: the installation owner sees every pod; anyone else
// sees a pod only as its workspace owner or an active member.
type Viewer struct {
	UserID string
	Owner  bool
}

const visible = `($2 OR w.owner_id = $1 OR EXISTS (SELECT 1 FROM workspace_members m
	WHERE m.workspace_id = w.id AND m.user_id = $1 AND m.status = 'active'))`

// ListFor returns the pods v may see.
func (s *Store) ListFor(ctx context.Context, v Viewer) ([]Pod, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+prefixed(podCols)+` FROM founderos_client_pods p
		JOIN workspaces w ON w.id = p.workspace_id WHERE `+visible+` ORDER BY p.client_name`, v.UserID, v.Owner)
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

// GetFor returns the pod if v may see it, else ErrNotFound (never "forbidden":
// a stranger learns nothing about which clients exist).
func (s *Store) GetFor(ctx context.Context, v Viewer, slug string) (Pod, error) {
	return scanPod(s.Pool.QueryRow(ctx, `SELECT `+prefixed(podCols)+` FROM founderos_client_pods p
		JOIN workspaces w ON w.id = p.workspace_id WHERE p.slug = $3 AND `+visible, v.UserID, v.Owner, slug))
}

// prefixed qualifies the pod columns with p. for joins.
func prefixed(cols string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = "p." + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}

// Settings are the pod's sending rules, editable by the owner.
type Settings struct {
	ApprovalPolicy                       string
	FirstEmailDailyCap, FollowUpDailyCap int
	Timezone                             string
	SendWindowStart, SendWindowEnd       int
	SendWeekends                         bool
	DryRunInbox, BookingURL              string
	CRMProvider, CalendarProvider        string
}

// UpdateSettings validates and saves the pod's sending rules.
func (s *Store) UpdateSettings(ctx context.Context, slug string, in Settings) error {
	bad := func(why string) error { return fmt.Errorf("%w: %s", ErrInvalid, why) }
	switch in.ApprovalPolicy {
	case "client_approves_each", "client_approves_sequences", "auto":
	default:
		return bad("approval policy must be client_approves_each, client_approves_sequences or auto")
	}
	if _, err := time.LoadLocation(in.Timezone); err != nil || in.Timezone == "" {
		return bad("timezone must be an IANA name such as Europe/London")
	}
	if in.SendWindowStart < 0 || in.SendWindowEnd > 24 || in.SendWindowStart >= in.SendWindowEnd {
		return bad("sending hours must be 0-24 with the start before the end")
	}
	if in.FirstEmailDailyCap < 0 || in.FirstEmailDailyCap > 500 || in.FollowUpDailyCap < 0 || in.FollowUpDailyCap > 1000 {
		return bad("daily caps must be 0-500 first emails and 0-1000 follow-ups")
	}
	sink := strings.TrimSpace(strings.ToLower(in.DryRunInbox))
	if sink != "" && (!strings.Contains(sink, "@") || strings.ContainsAny(sink, " <>")) {
		return bad("the test-run inbox must be an email address")
	}
	booking := strings.TrimSpace(in.BookingURL)
	if booking != "" && !strings.HasPrefix(booking, "https://") {
		return bad("the booking link must start with https://")
	}
	switch in.CRMProvider {
	case "", "hubspot", "pipedrive", "salesforce":
	default:
		return bad("CRM must be hubspot, pipedrive or salesforce")
	}
	switch in.CalendarProvider {
	case "", "calcom", "google", "microsoft":
	default:
		return bad("calendar must be calcom, google or microsoft")
	}
	n, err := s.lockedExec(ctx, slug, `UPDATE founderos_client_pods SET approval_policy = $2, first_email_daily_cap = $3,
		follow_up_daily_cap = $4, timezone = $5, send_window_start = $6, send_window_end = $7, send_weekends = $8,
		dry_run_inbox = $9, booking_url = $10, crm_provider = $11, calendar_provider = $12, updated_at = now(),
		config_changed_at = now() WHERE slug = $1`,
		slug, in.ApprovalPolicy, in.FirstEmailDailyCap, in.FollowUpDailyCap, in.Timezone, in.SendWindowStart, in.SendWindowEnd,
		in.SendWeekends, sink, booking, in.CRMProvider, in.CalendarProvider)
	if err == nil && n == 0 {
		return ErrNotFound
	}
	if err == nil {
		var id string
		if s.Pool.QueryRow(ctx, `SELECT id::text FROM founderos_client_pods WHERE slug = $1`, slug).Scan(&id) == nil {
			_ = s.Log(ctx, id, "settings", "Sending rules updated", "")
		}
	}
	return err
}
