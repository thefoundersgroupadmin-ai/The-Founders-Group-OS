package outbox

import (
	"context"
	"testing"

	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
)

const signedBody = "Hi Priya,\n\nCongratulations on opening the third Brightvale site. Keeping stock in sync across " +
	"locations is usually where spreadsheets start to hurt. Northwind gives you one live count across every " +
	"warehouse. Worth a 15-minute look on Wednesday?\n\nSam\n\nIf this isn't relevant, reply \"no thanks\" and I won't follow up."

func TestDispatchSendsOneScreenedEmailPerPodPerPassAndSkipsPodsNotRunning(t *testing.T) {
	pool := catalogdb.TestDB(t)
	ctx := context.Background()
	live := podFixture(t, pool, "client-live", "live")
	setup := podFixture(t, pool, "client-setup", "setup")
	if _, err := pool.Exec(ctx, `UPDATE founderos_client_pods SET sender_name = 'Sam'`); err != nil {
		t.Fatal(err)
	}
	for _, f := range []struct {
		file   func(string, string)
		id, to string
	}{
		{live, "x-1", "priya@brightvale.example"}, {live, "x-2", "jo@oakfield.example"}, {setup, "x-3", "kim@ridgeway.example"},
		{live, "x-4", "unsigned@nobody.example"},
	} {
		f.file(f.id, f.to)
	}
	if _, err := pool.Exec(ctx, `UPDATE founderos_outbox SET body = $1 WHERE id <> 'x-4'`, signedBody); err != nil {
		t.Fatal(err)
	}
	got, send := recorder()
	res, err := DispatchPods(ctx, pool, "thefoundersgroup.net", send)
	if err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 || (*got)[0].To != "priya@brightvale.example" || res.Sent != 1 {
		t.Fatalf("pass 1: %+v %+v", *got, res)
	}
	DispatchPods(ctx, pool, "thefoundersgroup.net", send)
	DispatchPods(ctx, pool, "thefoundersgroup.net", send)
	if len(*got) != 2 || status(t, pool, "x-3") != "pending" || status(t, pool, "x-4") != "pending" {
		t.Fatalf("after 3 passes sent %d; setup pod row %s; unsigned row %s", len(*got), status(t, pool, "x-3"), status(t, pool, "x-4"))
	}
	var why string
	_ = pool.QueryRow(ctx, `SELECT coalesce(error, '') FROM founderos_outbox WHERE id = 'x-4'`).Scan(&why)
	if why == "" {
		t.Fatal("a screened-out email does not say why it is waiting")
	}
}
