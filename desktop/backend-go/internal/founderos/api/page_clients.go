package api

// /clients (spec 6.13): client context, marketing requests and Superset
// workspaces, ported from FounderOS v1 app/clients/page.tsx and
// app/api/clients/work/route.ts.
//
//	GET  /api/founderos/pages/clients       page payload (roster, requests, volume)
//	POST /api/founderos/pages/clients/work  save a brief, or launch a saved one

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/rhl/businessos-backend/internal/founderos/pages/pods"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/rhl/businessos-backend/internal/founderos/connectors/superset"
	"github.com/rhl/businessos-backend/internal/founderos/guard"
	clientspage "github.com/rhl/businessos-backend/internal/founderos/pages/clients"
)

func init() { RegisterPage(registerClientsPage) }

const (
	clientsWindowDays = 14
	clientsMaxBody    = 30000
	clientsTokenName  = "CLIENT_WORK_OPERATOR_TOKEN"
)

var clientsStores sync.Map // *Deps → *clientspage.PgStore (keeps the workspace lookup)

// Swapped in tests.
var (
	clientsStoreFor = func(d *Deps) clientspage.Store {
		if s, ok := clientsStores.Load(d); ok {
			return s.(*clientspage.PgStore)
		}
		s, _ := clientsStores.LoadOrStore(d, clientspage.NewPgStore(d.Pool))
		return s.(*clientspage.PgStore)
	}
	clientsLauncherFor = func(d *Deps) clientspage.Launcher { return superset.New(d.Resolver) }
	clientsNow         = time.Now
)

var clientsUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func registerClientsPage(s *gin.RouterGroup, d *Deps) {
	s.GET("/pages/clients", func(c *gin.Context) { clientsPage(c, d) })
	s.POST("/pages/clients/work", func(c *gin.Context) { clientsWork(c, d) })
}

// clientsRoster is every client: the confirmed historical projects plus each AI SDR client from
// the client pods (one entry per client, however many pods it has), so a newly onboarded client
// appears here and can have work saved against it without a code change.
func clientsRoster(ctx context.Context, d *Deps) []clientspage.Project {
	out := append([]clientspage.Project{}, clientspage.Projects...)
	if d == nil || d.Pool == nil {
		return out
	}
	list, err := (&pods.Store{Pool: d.Pool}).List(ctx)
	if err != nil {
		return out
	}
	byName := map[string]*clientspage.Project{}
	var order []string
	for _, p := range list {
		if p.Status == pods.StatusHandedOver {
			continue
		}
		key := strings.ToLower(p.ClientName)
		cp, ok := byName[key]
		if !ok {
			cp = &clientspage.Project{ID: "pod-client-" + strings.TrimPrefix(pods.SlugFor(p.ClientName), "client-"), Name: p.ClientName,
				Service: "AI SDR", Retainer: packageName(p.Package) + " package"}
			byName[key] = cp
			order = append(order, key)
		}
		cp.Sources = append(cp.Sources, "/os/pods/"+p.Slug)
		cp.Context = fmt.Sprintf("%d AI SDR pod(s); latest status %s.", len(cp.Sources), strings.ReplaceAll(p.Status, "_", " "))
		if cp.ProjectName == "" {
			cp.ProjectName = p.Slug
		}
	}
	for _, k := range order {
		out = append(out, *byName[k])
	}
	return out
}

// packageName is the package as sold today (USD names; "custom" became Starter).
func packageName(p string) string {
	switch strings.ToLower(p) {
	case "custom", "starter":
		return "Starter"
	case "core":
		return "Core"
	case "full":
		return "The Full"
	}
	return p
}

func clientRefs(list []clientspage.Project) []clientspage.ClientRef {
	out := make([]clientspage.ClientRef, 0, len(list))
	for _, p := range list {
		out = append(out, clientspage.ClientRef{ID: p.ID, Name: p.Name})
	}
	return out
}

func clientsPage(c *gin.Context, d *Deps) {
	work, err := clientsStoreFor(d).All(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "client work is unreadable: " + err.Error()})
		return
	}
	roster := clientsRoster(c.Request.Context(), d)
	c.JSON(http.StatusOK, gin.H{
		"clients":       roster,
		"work":          work,
		"volume":        clientspage.ComputeVolume(clientRefs(roster), work, clientsNow(), clientsWindowDays),
		"windowDays":    clientsWindowDays,
		"statusOrder":   clientspage.StatusOrder,
		"slackBridge":   "not activated",
		"launchEnabled": guard.WritesEnabled(),
	})
}

type clientsWorkInput struct {
	Action   string `json:"action"`
	ClientID string `json:"clientId"`
	Brief    string `json:"brief"`
	ID       string `json:"id"`
}

const clientsBadInput = "Provide a valid client and a brief of 5 to 6,000 characters."

func clientsWork(c *gin.Context, d *Deps) {
	// Session auth and BusinessOS's double-submit CSRF stand in for the operator
	// OS's same-origin check.
	if c.Request.ContentLength > clientsMaxBody {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "Request too large."})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, clientsMaxBody+1))
	if err != nil || len(raw) > clientsMaxBody {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "Request too large."})
		return
	}
	var in clientsWorkInput
	if json.Unmarshal(raw, &in) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": clientsBadInput})
		return
	}
	ctx := c.Request.Context()
	store := clientsStoreFor(d)

	switch in.Action {
	case "save":
		brief := strings.TrimSpace(in.Brief)
		if n := len([]rune(brief)); n < 5 || n > 6000 {
			c.JSON(http.StatusBadRequest, gin.H{"error": clientsBadInput})
			return
		}
		known := false
		for _, p := range clientsRoster(ctx, d) {
			known = known || p.ID == in.ClientID
		}
		if !known {
			c.JSON(http.StatusNotFound, gin.H{"error": "Unknown client."})
			return
		}
		w := clientspage.Work{ID: uuid.NewString(), ClientID: in.ClientID, Brief: brief, Status: clientspage.StatusSaved,
			CreatedAt: clientsNow().UTC().Format("2006-01-02T15:04:05.000Z")}
		if err := store.Add(ctx, w); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "The request could not be saved: " + err.Error()})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"work": w})
	case "launch":
		if !clientsUUID.MatchString(in.ID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": clientsBadInput})
			return
		}
		expected := d.Resolver.Resolve(clientsTokenName)
		supplied := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		if len(expected) < 32 || subtle.ConstantTimeCompare([]byte(supplied), []byte(expected)) != 1 {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "An operator token is required to launch agents. Configure CLIENT_WORK_OPERATOR_TOKEN on the execution host."})
			return
		}
		w, err := store.Get(ctx, in.ID)
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "client work is unreadable: " + err.Error()})
			return
		}
		if w == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Request not found."})
			return
		}
		// Launching starts a paid agent on the host: an outbound side effect.
		// With FOUNDEROS_WRITES=0 it is refused and logged before anything is
		// claimed, so the request stays saved.
		if err := guard.Outbound("clients.work.launch", func() error { return nil }); errors.Is(err, guard.ErrWritesDisabled) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Launching is off on the bridge (FOUNDEROS_WRITES=0). The request stays saved; nothing was started.", "work": w, "guarded": true})
			return
		}
		ok, err := store.Claim(ctx, w.ID)
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "client work is unreadable: " + err.Error()})
			return
		}
		if !ok {
			c.JSON(http.StatusConflict, gin.H{"error": "This request has already been submitted. Check its workspace before retrying."})
			return
		}
		id, lerr := clientspage.Launch(ctx, clientsLauncherFor(d), w.ClientID, w.ID, w.Brief)
		var ws *string
		if lerr == nil {
			ws = &id
		}
		ferr := store.Finish(ctx, w.ID, ws)
		after, _ := store.Get(ctx, w.ID)
		if lerr != nil || ferr != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "Superset could not confirm the launch. The client project must be on this host. Check Superset before retrying.", "work": after})
			return
		}
		c.JSON(http.StatusOK, gin.H{"work": after})
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": clientsBadInput})
	}
}
