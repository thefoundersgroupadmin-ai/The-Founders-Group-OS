package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/rhl/businessos-backend/internal/founderos/notify"
)

func TestDoctorNoticesArePublishedOnceAndRemoved(t *testing.T) {
	root := t.TempDir()
	t.Setenv("FOUNDEROS_PEOPLE_DIR", filepath.Join(root, "people"))
	dir := filepath.Join(root, "doctor", "notify")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "a.json"), []byte(`{"id":"doctor-a","title":"Needs you: Website","body":"down"}`), 0o600)
	os.WriteFile(filepath.Join(dir, "b.json"), []byte(`not json`), 0o600)

	var got []notify.Event
	n := publishDoctorNotices(context.Background(), func(_ context.Context, e notify.Event) { got = append(got, e) })
	if n != 1 || len(got) != 1 || got[0].Title != "Needs you: Website" || got[0].Kind != notify.KindHealth || got[0].URL != "/os/doctor" {
		t.Fatalf("published %d %+v", n, got)
	}
	left, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(left) != 0 {
		t.Fatalf("notices left behind: %v", left)
	}
	if publishDoctorNotices(context.Background(), func(context.Context, notify.Event) { t.Fatal("sent twice") }) != 0 {
		t.Fatal("sent twice")
	}
}

func TestDoctorRepairStateReadsValidJSONOnly(t *testing.T) {
	root := t.TempDir()
	t.Setenv("FOUNDEROS_PEOPLE_DIR", filepath.Join(root, "people"))
	if doctorRepairState() != nil {
		t.Fatal("want nil before the first pass")
	}
	os.MkdirAll(filepath.Join(root, "doctor"), 0o700)
	os.WriteFile(filepath.Join(root, "doctor", "state.json"), []byte(`{"lastPass":"2026-11-10T18:00:00Z","checks":{}}`), 0o600)
	if got := doctorRepairState(); got == nil || string(got)[:12] != `{"lastPass":` {
		t.Fatalf("got %s", got)
	}
}
