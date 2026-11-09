package api

// The client's own CRM per pod (docs/tfg/CLIENT-PODS.md, M5).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/rhl/businessos-backend/internal/founderos/connectors/email"
	"github.com/rhl/businessos-backend/internal/founderos/pages/crmsync"
	"github.com/rhl/businessos-backend/internal/founderos/pages/integrations"
	"github.com/rhl/businessos-backend/internal/founderos/pages/pods"
)

// podCRM builds the pod's CRM from its keys, or nil when not connected.
func podCRM(d *Deps, p pods.Pod) crmsync.CRM {
	key := func(k string) string { return strings.TrimSpace(d.Resolver.Resolve(email.PodKeyPrefix(p.Slug) + k)) }
	switch p.CRMProvider {
	case "hubspot":
		if t := key("HUBSPOT_TOKEN"); t != "" {
			return crmsync.HubSpot{Token: t}
		}
	case "pipedrive":
		if t := key("PIPEDRIVE_TOKEN"); t != "" {
			return crmsync.Pipedrive{Token: t}
		}
	case "salesforce":
		if id, sec, rt := key("SALESFORCE_CLIENT_ID"), key("SALESFORCE_CLIENT_SECRET"), key("SALESFORCE_REFRESH_TOKEN"); id != "" && sec != "" && rt != "" {
			return &crmsync.Salesforce{ClientID: id, ClientSecret: sec, RefreshToken: rt, LoginURL: key("SALESFORCE_LOGIN_URL")}
		}
	}
	return nil
}

// syncPodCRMs writes each connected pod's work into its client's CRM.
func syncPodCRMs(ctx context.Context, d *Deps, st *pods.Store) (int, error) {
	list, err := st.List(ctx)
	if err != nil {
		return 0, err
	}
	total := 0
	var errs []error
	for _, p := range list {
		// Only real work reaches the client's CRM: never a test run's contacts or
		// simulated emails, and nothing after hand-over (Codex final review #7).
		if p.Status != pods.StatusLive && p.Status != pods.StatusPaused {
			continue
		}
		// Under the pod's CRM lock, with the pod and its connection read afresh:
		// a connection change is never mixed with the old one's records.
		unlock := lockPodCRM(p.ID)
		fresh, err := st.Get(ctx, p.Slug)
		if err != nil || (fresh.Status != pods.StatusLive && fresh.Status != pods.StatusPaused) {
			unlock()
			continue
		}
		crm := podCRM(d, fresh)
		if crm == nil {
			unlock()
			continue
		}
		n, err := crmsync.SyncPod(ctx, d.Pool, p.ID, crm)
		unlock()
		total += n
		if err != nil {
			errs = append(errs, fmt.Errorf("%s (%s): %w", p.Slug, crm.Name(), err))
			_ = st.Log(ctx, p.ID, "crm_error", "CRM sync stopped: "+err.Error(), "")
		} else if n > 0 {
			_ = st.Log(ctx, p.ID, "crm_synced", fmt.Sprintf("%d updates written to your %s", n, crm.Name()), "")
		}
	}
	return total, errors.Join(errs...)
}

func registerPodCRM(s *gin.RouterGroup, d *Deps, pod func(*gin.Context) (*pods.Store, pods.Pod, bool), reply func(*gin.Context, *pods.Store, string)) {
	s.PUT("/pods/:slug/crm", func(c *gin.Context) {
		st, p, ok := pod(c)
		if !ok {
			return
		}
		if who, _ := personOf(c); !who.Owner {
			c.JSON(http.StatusForbidden, gin.H{"error": "only the TFG owner connects a client's CRM"})
			return
		}
		var in struct {
			Provider, Token, ClientID, ClientSecret, RefreshToken, LoginURL string
		}
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "provider and credentials expected"})
			return
		}
		values := map[string]string{}
		pre := email.PodKeyPrefix(p.Slug)
		switch in.Provider {
		case "hubspot":
			values[pre+"HUBSPOT_TOKEN"] = in.Token
		case "pipedrive":
			values[pre+"PIPEDRIVE_TOKEN"] = in.Token
		case "salesforce":
			values[pre+"SALESFORCE_CLIENT_ID"] = in.ClientID
			values[pre+"SALESFORCE_CLIENT_SECRET"] = in.ClientSecret
			values[pre+"SALESFORCE_REFRESH_TOKEN"] = in.RefreshToken
			if in.LoginURL != "" {
				if !strings.HasPrefix(in.LoginURL, "https://") {
					c.JSON(http.StatusBadRequest, gin.H{"error": "the Salesforce login URL must start with https://"})
					return
				}
				values[pre+"SALESFORCE_LOGIN_URL"] = in.LoginURL
			}
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "provider must be hubspot, pipedrive or salesforce"})
			return
		}
		for k, v := range values {
			v = strings.TrimSpace(v)
			if v == "" || !integrationsSafeValue(v) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "missing or unsafe value for " + strings.ToLower(strings.TrimPrefix(k, pre))})
				return
			}
			values[k] = v
		}
		path, ok := integrationsEnvLocal(c, d)
		if !ok {
			return
		}
		// No sync runs while the connection changes (Codex fix review #4).
		unlock := lockPodCRM(p.ID)
		defer unlock()
		// Another CRM: record ids from the old one must never be written to, so
		// the mapping starts afresh. Same CRM (e.g. a rotated token): the mapping
		// stays, so nothing already logged is written twice.
		if p.CRMProvider != "" && p.CRMProvider != in.Provider {
			if _, err := d.Pool.Exec(c.Request.Context(), `DELETE FROM founderos_pod_crm_ids WHERE pod_id = $1`, p.ID); err != nil {
				podError(c, err)
				return
			}
			_ = st.Log(c.Request.Context(), p.ID, "settings", "CRM changed from "+p.CRMProvider+" to "+in.Provider+"; records are written to the new CRM from scratch", "")
		}
		if err := integrations.UpsertEnv(path, values); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "could not write ~/.founderos/.env"})
			return
		}
		if _, err := d.Pool.Exec(c.Request.Context(), `UPDATE founderos_client_pods SET crm_provider = $2, updated_at = now() WHERE slug = $1`, p.Slug, in.Provider); err != nil {
			podError(c, err)
			return
		}
		_ = st.Log(c.Request.Context(), p.ID, "settings", "CRM connected ("+in.Provider+")", "")
		c.Header("Cache-Control", "no-store")
		reply(c, st, p.Slug)
	})
	s.GET("/pods/:slug/crm", func(c *gin.Context) {
		_, p, ok := pod(c)
		if !ok {
			return
		}
		var n int
		_ = d.Pool.QueryRow(c.Request.Context(), `SELECT count(*) FROM founderos_pod_crm_ids WHERE pod_id = $1`, p.ID).Scan(&n)
		c.JSON(http.StatusOK, gin.H{"provider": p.CRMProvider, "connected": podCRM(d, p) != nil, "synced": n})
	})
}

// podCRMLocks keeps a pod's CRM sync and a change of its CRM connection apart.
var podCRMLocks sync.Map

func lockPodCRM(podID string) func() {
	m, _ := podCRMLocks.LoadOrStore(podID, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}
