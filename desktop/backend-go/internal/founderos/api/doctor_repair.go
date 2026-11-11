package api

// Background scripts (the auto-repair doctor, the website enquiry import) drop notices in
// ~/.founderos/doctor/notify; a notice may name its kind (e.g. "reply"), health otherwise.
// The auto-repair doctor (scripts/tfg-doctor.py, launchd net.thefoundersgroup.tfg-doctor)
// keeps its state in ~/.founderos/doctor. The backend shows that state on /os/doctor
// and sends the doctor's notices (notify/*.json: "needs you", "fixed by itself") to the
// owner's phone, then removes them.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"github.com/rhl/businessos-backend/internal/founderos/notify"
	"github.com/rhl/businessos-backend/internal/founderos/people"
)

func doctorDir() string { return filepath.Join(filepath.Dir(people.Root()), "doctor") }

// doctorRepairState is the doctor's state.json as written, or nil before its first pass.
func doctorRepairState() json.RawMessage {
	raw, err := os.ReadFile(filepath.Join(doctorDir(), "state.json"))
	if err != nil || !json.Valid(raw) {
		return nil
	}
	return raw
}

type doctorNotice struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"`
}

// publishDoctorNotices sends and removes each waiting notice, oldest first.
// Returns how many were sent. A malformed file is removed so it can't block the queue.
func publishDoctorNotices(ctx context.Context, publish func(context.Context, notify.Event)) int {
	dir := filepath.Join(doctorDir(), "notify")
	paths, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	sort.Strings(paths)
	sent := 0
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var n doctorNotice
		if json.Unmarshal(raw, &n) == nil && n.ID != "" && n.Title != "" {
			url := n.URL
			if url == "" {
				url = "/os/doctor"
			}
			kind := notify.KindHealth
			for _, k := range notify.Kinds {
				if n.Kind == k {
					kind = k
				}
			}
			publish(ctx, notify.Event{ID: n.ID, Kind: kind, Title: n.Title, Body: notifyClip(n.Body, 300), URL: url})
			sent++
		}
		_ = os.Remove(p)
	}
	return sent
}
