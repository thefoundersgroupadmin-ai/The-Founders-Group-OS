package outbox

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
)

func seed(t *testing.T, pool *pgxpool.Pool, ws, id string, contactID any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `INSERT INTO founderos_outbox
		(id, workspace_id, source, source_ref, contact_id, to_address, to_name, company, subject, body)
		VALUES ($1, $2, 'paperclip', 'THE-12', $3, 'eren@hitratech.example', 'Eren Can', 'Hitratech', 'Hello', 'Body')`,
		id, ws, contactID); err != nil {
		t.Fatal(err)
	}
}

func status(t *testing.T, pool *pgxpool.Pool, id string) string {
	t.Helper()
	var s string
	if err := pool.QueryRow(context.Background(), `SELECT status FROM founderos_outbox WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

var reviewed = Content{Subject: "Hello", Body: "Body"}

func TestLoadSplitsWaitingFromDecidedAndScopesToTheWorkspace(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	ws := catalogdb.Workspace(t, pool, "founderos")
	other := catalogdb.Workspace(t, pool, "vantage")
	seed(t, pool, ws, "ob-1", nil)
	seed(t, pool, ws, "ob-2", nil)
	seed(t, pool, other, "ob-x", nil)
	if _, err := Reject(ctx, pool, "founderos", "ob-2"); err != nil {
		t.Fatal(err)
	}
	p, err := Load(ctx, pool, "founderos")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Pending) != 1 || p.Pending[0].ID != "ob-1" || len(p.Recent) != 1 || p.Recent[0].Status != "rejected" {
		t.Fatalf("page = %+v", p)
	}
}

func TestApproveSendsAtMostOnceUnderConcurrentClicks(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	ws := catalogdb.Workspace(t, pool, "founderos")
	seed(t, pool, ws, "ob-1", nil)
	var sends atomic.Int32
	send := func(context.Context, Item) error { sends.Add(1); return nil }
	var wg sync.WaitGroup
	errs := make([]error, 5)
	for i := range errs {
		wg.Add(1)
		go func(i int) { defer wg.Done(); _, errs[i] = Approve(ctx, pool, "founderos", "ob-1", reviewed, send) }(i)
	}
	wg.Wait()
	if sends.Load() != 1 {
		t.Fatalf("sent %d times, want exactly once", sends.Load())
	}
	refused := 0
	for _, err := range errs {
		if errors.Is(err, ErrNotPending) {
			refused++
		}
	}
	if refused != 4 || status(t, pool, "ob-1") != "sent" {
		t.Fatalf("refused=%d status=%s errs=%v", refused, status(t, pool, "ob-1"), errs)
	}
}

func TestApproveSendsExactlyWhatTheOperatorReviewed(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	ws := catalogdb.Workspace(t, pool, "founderos")
	seed(t, pool, ws, "ob-1", nil)
	// An agent revises the draft after the operator loaded the page.
	if _, err := pool.Exec(ctx, `UPDATE founderos_outbox SET body = 'Sneaky new body' WHERE id = 'ob-1'`); err != nil {
		t.Fatal(err)
	}
	var got Item
	if _, err := Approve(ctx, pool, "founderos", "ob-1", Content{Subject: "  Edited subject ", Body: "Body"},
		func(_ context.Context, it Item) error { got = it; return nil }); err != nil {
		t.Fatal(err)
	}
	if got.Subject != "Edited subject" || got.Body != "Body" {
		t.Fatalf("sent %q / %q, want the reviewed content", got.Subject, got.Body)
	}
	if _, err := Approve(ctx, pool, "founderos", "ob-1", Content{Subject: " ", Body: "b"}, nil); !errors.Is(err, ErrBadContent) {
		t.Fatalf("blank subject err = %v", err)
	}
}

func TestAFailedSendIsRecordedAndCanBeRetried(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	ws := catalogdb.Workspace(t, pool, "founderos")
	seed(t, pool, ws, "ob-1", nil)
	boom := errors.New("smtp: 535 auth failed")
	it, err := Approve(ctx, pool, "founderos", "ob-1", reviewed, func(context.Context, Item) error { return boom })
	if !errors.Is(err, boom) || it.Status != "failed" || it.Error == nil || *it.Error != boom.Error() {
		t.Fatalf("it=%+v err=%v", it, err)
	}
	if _, err := Approve(ctx, pool, "founderos", "ob-1", reviewed, func(context.Context, Item) error { return nil }); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if status(t, pool, "ob-1") != "sent" {
		t.Fatalf("status after retry = %s", status(t, pool, "ob-1"))
	}
}

func TestRejectAndMissingRows(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	ws := catalogdb.Workspace(t, pool, "founderos")
	seed(t, pool, ws, "ob-1", nil)
	if _, err := Reject(ctx, pool, "founderos", "ob-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := Approve(ctx, pool, "founderos", "ob-1", reviewed, func(context.Context, Item) error {
		t.Fatal("a rejected email was sent")
		return nil
	}); !errors.Is(err, ErrNotPending) {
		t.Fatalf("approve after reject err = %v", err)
	}
	if _, err := Reject(ctx, pool, "founderos", "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing row err = %v", err)
	}
}

func TestASentEmailIsLoggedOnTheContactsPipeline(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	ws := catalogdb.Workspace(t, pool, "founderos")
	venture := catalogdb.Workspace(t, pool, "vantage")
	if _, err := pool.Exec(ctx, `INSERT INTO founderos_funnel_contacts (id, workspace_id, name, venture, status, created_at)
		VALUES ('fc-pc-hitratech', $1, 'Eren Can', 'vantage', 'first_touch', now())`, venture); err != nil {
		t.Fatal(err)
	}
	seed(t, pool, ws, "ob-1", "fc-pc-hitratech")
	if _, err := Approve(ctx, pool, "founderos", "ob-1", reviewed, func(context.Context, Item) error { return nil }); err != nil {
		t.Fatal(err)
	}
	var channel, label, stage, touchWS string
	if err := pool.QueryRow(ctx, `SELECT channel, label, stage, workspace_id::text FROM founderos_funnel_touches
		WHERE contact_id = 'fc-pc-hitratech'`).Scan(&channel, &label, &stage, &touchWS); err != nil {
		t.Fatal(err)
	}
	if channel != "email" || label != "Outreach sent: Hello" || stage != "first_touch" || touchWS != venture {
		t.Fatalf("touch = %s/%s/%s/%s", channel, label, stage, touchWS)
	}
}

func TestAnUncertainSendIsNeverRetriedWithoutForce(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	ws := catalogdb.Workspace(t, pool, "founderos")
	seed(t, pool, ws, "ob-1", nil)
	lost := fmt.Errorf("%w: connection reset after DATA", ErrUncertain)
	it, err := Approve(ctx, pool, "founderos", "ob-1", reviewed, func(context.Context, Item) error { return lost })
	if !errors.Is(err, ErrUncertain) || it.Status != "uncertain" {
		t.Fatalf("it=%+v err=%v", it, err)
	}
	if _, err := Approve(ctx, pool, "founderos", "ob-1", reviewed, func(context.Context, Item) error {
		t.Fatal("an uncertain email was resent without force")
		return nil
	}); !errors.Is(err, ErrNotPending) {
		t.Fatalf("unforced retry err = %v", err)
	}
	forced := reviewed
	forced.Force = true
	if it, err := Approve(ctx, pool, "founderos", "ob-1", forced, func(context.Context, Item) error { return nil }); err != nil || it.Attempt != 2 {
		t.Fatalf("forced retry: it=%+v err=%v", it, err)
	}
}

func TestAStaleSendingClaimBecomesUncertain(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	ws := catalogdb.Workspace(t, pool, "founderos")
	seed(t, pool, ws, "ob-1", nil)
	seed(t, pool, ws, "ob-2", nil)
	if _, err := pool.Exec(ctx, `UPDATE founderos_outbox SET status = 'sending', decided_at = now() - interval '11 minutes' WHERE id = 'ob-1';
		UPDATE founderos_outbox SET status = 'sending', decided_at = now() WHERE id = 'ob-2'`); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(ctx, pool, "founderos"); err != nil {
		t.Fatal(err)
	}
	if status(t, pool, "ob-1") != "uncertain" || status(t, pool, "ob-2") != "sending" {
		t.Fatalf("ob-1=%s ob-2=%s", status(t, pool, "ob-1"), status(t, pool, "ob-2"))
	}
}

func TestACancelledRequestStillRecordsTheSend(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ws := catalogdb.Workspace(t, pool, "founderos")
	seed(t, pool, ws, "ob-1", nil)
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := Approve(ctx, pool, "founderos", "ob-1", reviewed, func(sendCtx context.Context, _ Item) error {
		cancel() // the browser tab closes mid-send
		time.Sleep(10 * time.Millisecond)
		return sendCtx.Err()
	}); err != nil {
		t.Fatalf("the send context followed the request: %v", err)
	}
	if status(t, pool, "ob-1") != "sent" {
		t.Fatalf("status = %s", status(t, pool, "ob-1"))
	}
}
