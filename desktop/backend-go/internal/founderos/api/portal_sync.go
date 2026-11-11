package api

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/rhl/businessos-backend/internal/founderos/notify"
	"github.com/rhl/businessos-backend/internal/founderos/pages/pods"
	"github.com/rhl/businessos-backend/internal/founderos/pages/portalsync"
)

var portalSyncOnce sync.Once

// retentionDay is the last day the portal retention ran successfully.
var retentionDay string

// startPortalSync keeps client pods and the client portal in step every two
// minutes once the portal URL and service key are set. Never under go test.
func startPortalSync(d *Deps) {
	if d.Pool == nil || testing.Testing() {
		return
	}
	portalSyncOnce.Do(func() {
		go func() {
			t := time.NewTicker(2 * time.Minute)
			defer t.Stop()
			for range t.C {
				rest, ok := portalsync.NewREST(func(k string) string { return d.Resolver.Resolve(k) })
				if !ok {
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				// Once a day: the portal's data retention (ended clients' files, then their records).
				if day := time.Now().Format("2006-01-02"); day != retentionDay {
					if msg, err := rest.Retention(ctx); err != nil {
						slog.Warn("client portal retention", "error", err)
					} else {
						retentionDay = day
						slog.Info("client portal retention", "result", msg)
					}
				}
				res, err := portalsync.Sync(ctx, &pods.Store{Pool: d.Pool}, rest)
				if err != nil {
					slog.Warn("client portal sync", "error", err)
				}
				// A client's message in the portal reaches the owner's phone straight away.
				for _, m := range res.Messages {
					name := res.Clients[m.ClientID]
					notify.Publish(ctx, notify.Event{ID: "portal-msg-" + m.ID, Kind: notify.KindReply,
						Title: "Client message: " + name, Body: notifyClip(m.Body, 220), URL: "/os/pods"})
				}
				cancel()
			}
		}()
	})
}
