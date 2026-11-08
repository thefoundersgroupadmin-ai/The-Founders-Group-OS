package api

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/rhl/businessos-backend/internal/founderos/pages/pods"
	"github.com/rhl/businessos-backend/internal/founderos/pages/portalsync"
)

var portalSyncOnce sync.Once

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
				if err := portalsync.Sync(ctx, &pods.Store{Pool: d.Pool}, rest); err != nil {
					slog.Warn("client portal sync", "error", err)
				}
				cancel()
			}
		}()
	})
}
