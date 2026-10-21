package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rhl/businessos-backend/internal/founderos/connectors/email"
	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
)

func outboxRouter(t *testing.T, send func(context.Context, email.Reply) error) (http.Handler, func(id string)) {
	t.Helper()
	pool := catalogdb.TestDB(t)
	ws := catalogdb.Workspace(t, pool, "founderos")
	h := commsRouter(t, pool, &commsSources{SendEmail: send})
	return h, func(id string) {
		if _, err := pool.Exec(context.Background(), `INSERT INTO founderos_outbox
			(id, workspace_id, source, to_address, subject, body, account_id)
			VALUES ($1, $2, 'paperclip', 'eren@hitratech.example', 'Hello Eren', 'Body', 'inbox-1')`, id, ws); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOutboxPageNeedsASession(t *testing.T) {
	h, _ := outboxRouter(t, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/founderos/pages/outbox", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no session: %d", w.Code)
	}
}

func TestOutboxApproveSendsAsAnApprovedRecipientOnce(t *testing.T) {
	var sent []email.Reply
	h, seed := outboxRouter(t, func(_ context.Context, r email.Reply) error { sent = append(sent, r); return nil })
	seed("ob-1")
	if code, body := commsDo(t, h, http.MethodGet, "/api/founderos/pages/outbox", ""); code != http.StatusOK || len(body["pending"].([]any)) != 1 {
		t.Fatalf("list: %d %v", code, body)
	}
	if code, body := commsDo(t, h, http.MethodPost, "/api/founderos/pages/outbox/ob-1/approve", `{"subject":"Edited","body":"Body"}`); code != http.StatusOK || body["ok"] != true {
		t.Fatalf("approve: %d %v", code, body)
	}
	if len(sent) != 1 || sent[0].To != "eren@hitratech.example" || sent[0].Subject != "Edited" || !sent[0].RecipientApproved || sent[0].AccountID != "inbox-1" {
		t.Fatalf("sent = %+v", sent)
	}
	if code, _ := commsDo(t, h, http.MethodPost, "/api/founderos/pages/outbox/ob-1/approve", `{"subject":"Edited","body":"Body"}`); code != http.StatusConflict {
		t.Fatalf("second approve: %d, want 409", code)
	}
	if len(sent) != 1 {
		t.Fatalf("a second approve sent again: %d sends", len(sent))
	}
}

func TestOutboxFailedSendIsReportedAndRejectWorks(t *testing.T) {
	h, seed := outboxRouter(t, func(context.Context, email.Reply) error { return errors.New("blocked by mail-guard") })
	seed("ob-1")
	seed("ob-2")
	code, body := commsDo(t, h, http.MethodPost, "/api/founderos/pages/outbox/ob-1/approve", `{"subject":"Hello Eren","body":"Body"}`)
	if code != http.StatusBadGateway || body["ok"] != false || body["item"].(map[string]any)["status"] != "failed" {
		t.Fatalf("failed send: %d %v", code, body)
	}
	if code, body := commsDo(t, h, http.MethodPost, "/api/founderos/pages/outbox/ob-2/reject", ""); code != http.StatusOK || body["item"].(map[string]any)["status"] != "rejected" {
		t.Fatalf("reject: %d %v", code, body)
	}
	if code, _ := commsDo(t, h, http.MethodPost, "/api/founderos/pages/outbox/missing/reject", ""); code != http.StatusNotFound {
		t.Fatalf("missing: %d", code)
	}
	if code, _ := commsDo(t, h, http.MethodPost, "/api/founderos/pages/outbox/ob-2/approve", `{"subject":"s","body":"   "}`); code != http.StatusBadRequest {
		t.Fatalf("blank body: %d", code)
	}
	if code, _ := commsDo(t, h, http.MethodPost, "/api/founderos/pages/outbox/ob-2/approve", ""); code != http.StatusBadRequest {
		t.Fatalf("approve without the reviewed content: %d", code)
	}
}

func TestOutboxMapsAnUncertainSMTPDeliveryToUncertain(t *testing.T) {
	h, seed := outboxRouter(t, func(context.Context, email.Reply) error {
		return fmt.Errorf("%w: 451 after DATA", email.ErrDeliveryUncertain)
	})
	seed("ob-1")
	code, body := commsDo(t, h, http.MethodPost, "/api/founderos/pages/outbox/ob-1/approve", `{"subject":"Hello Eren","body":"Body"}`)
	if code != http.StatusBadGateway || body["item"].(map[string]any)["status"] != "uncertain" {
		t.Fatalf("uncertain: %d %v", code, body)
	}
}
