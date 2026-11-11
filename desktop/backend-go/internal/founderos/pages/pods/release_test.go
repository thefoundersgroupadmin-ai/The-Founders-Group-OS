package pods

import (
	"context"
	"testing"

	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
)

// Adding the rep's email later sends the briefs and reviews that had nobody to go to.
func TestAddingARepEmailReleasesUnaddressedNotices(t *testing.T) {
	s := &Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	p := newPod(t, s, "Repless")
	if _, err := s.Pool.Exec(ctx, `INSERT INTO founderos_pod_weekly (pod_id, week_start, metrics, review, review_delivery, review_sent_at)
		VALUES ($1, '2026-11-02', '{}', 'Good week', 'no_recipient', now())`, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateSettings(ctx, p.Slug, Settings{ApprovalPolicy: "auto", Timezone: "UTC", SendWindowStart: 9, SendWindowEnd: 17,
		FirstEmailDailyCap: 10, FollowUpDailyCap: 10, HandoffEmail: "rep@repless.example"}); err != nil {
		t.Fatal(err)
	}
	var state string
	var sentAt *string
	if err := s.Pool.QueryRow(ctx, `SELECT review_delivery, review_sent_at::text FROM founderos_pod_weekly WHERE pod_id = $1`, p.ID).Scan(&state, &sentAt); err != nil {
		t.Fatal(err)
	}
	if state != "held" || sentAt != nil {
		t.Fatalf("review still unaddressed: %s %v", state, sentAt)
	}
}
