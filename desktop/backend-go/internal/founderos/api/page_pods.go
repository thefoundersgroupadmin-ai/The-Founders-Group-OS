package api

// /api/founderos/pods: client AI SDR pods (docs/tfg/CLIENT-PODS.md). Only the
// installation owner creates pods; everyone else reaches a pod only as a
// member of its workspace, and a pod they may not see answers 404.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rhl/businessos-backend/internal/founderos/connectors/caldotcom"
	"github.com/rhl/businessos-backend/internal/founderos/connectors/email"
	"github.com/rhl/businessos-backend/internal/founderos/connectors/fathomcalls"
	"github.com/rhl/businessos-backend/internal/founderos/pages/integrations"
	"github.com/rhl/businessos-backend/internal/founderos/pages/outbox"
	"github.com/rhl/businessos-backend/internal/founderos/pages/pods"
	"github.com/rhl/businessos-backend/internal/founderos/pages/portalsync"
)

func init() { RegisterPage(registerPods) }

type podJSON struct {
	Slug               string     `json:"slug"`
	ClientName         string     `json:"clientName"`
	Package            string     `json:"package"`
	Reps               int        `json:"reps"`
	Status             string     `json:"status"`
	SenderName         string     `json:"senderName"`
	SenderEmail        string     `json:"senderEmail"`
	SendingDomain      string     `json:"sendingDomain"`
	ApprovalPolicy     string     `json:"approvalPolicy"`
	FirstEmailDailyCap int        `json:"firstEmailDailyCap"`
	FollowUpDailyCap   int        `json:"followUpDailyCap"`
	Timezone           string     `json:"timezone"`
	SendWindow         [2]int     `json:"sendWindow"`
	SendWeekends       bool       `json:"sendWeekends"`
	DryRunInbox        string     `json:"dryRunInbox"`
	CRMProvider        string     `json:"crmProvider"`
	CalendarProvider   string     `json:"calendarProvider"`
	BookingURL         string     `json:"bookingUrl"`
	PausedReason       string     `json:"pausedReason"`
	PortalClientID     string     `json:"portalClientId"`
	HandoffEmail       string     `json:"handoffEmail"`
	InboundAutoReply   bool       `json:"inboundAutoReply"`
	InboundLinkSet     bool       `json:"inboundLinkSet"`
	WentLiveAt         *time.Time `json:"wentLiveAt"`
	Missing            []string   `json:"missingToGoLive,omitempty"`
	// Go-live requirements waived for this client, with the reason, who and when.
	Waivers map[string]map[string]string `json:"readinessWaivers,omitempty"`
}

func podOut(p pods.Pod) podJSON {
	return podJSON{Slug: p.Slug, ClientName: p.ClientName, Package: p.Package, Reps: p.Reps, Status: p.Status,
		SenderName: p.SenderName, SenderEmail: p.SenderEmail, SendingDomain: p.SendingDomain,
		ApprovalPolicy: p.ApprovalPolicy, FirstEmailDailyCap: p.FirstEmailDailyCap, FollowUpDailyCap: p.FollowUpDailyCap,
		Timezone: p.Timezone, SendWindow: [2]int{p.SendWindowStart, p.SendWindowEnd}, SendWeekends: p.SendWeekends, DryRunInbox: p.DryRunInbox,
		CRMProvider: p.CRMProvider, CalendarProvider: p.CalendarProvider, BookingURL: p.BookingURL,
		PausedReason: p.PausedReason, WentLiveAt: p.WentLiveAt, PortalClientID: deref(p.PortalClientID),
		HandoffEmail: p.HandoffEmail, InboundAutoReply: p.InboundAutoReply, InboundLinkSet: p.InboundLinkSet}
}

func registerPods(s *gin.RouterGroup, d *Deps) {
	startPortalSync(d)
	startAgentRefresh(d)
	startPodLoops(d)
	store := func(c *gin.Context) (*pods.Store, pods.Viewer, bool) {
		p, ok := personOf(c)
		if !ok {
			return nil, pods.Viewer{}, false
		}
		if d.Pool == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "client pods need the OS database"})
			return nil, pods.Viewer{}, false
		}
		return &pods.Store{Pool: d.Pool}, pods.Viewer{UserID: p.ID, Owner: p.Owner}, true
	}
	// pod loads the :slug pod the caller may see, answering 404 otherwise.
	pod := func(c *gin.Context) (*pods.Store, pods.Pod, bool) {
		st, v, ok := store(c)
		if !ok {
			return nil, pods.Pod{}, false
		}
		p, err := st.GetFor(c.Request.Context(), v, c.Param("slug"))
		if err != nil {
			podError(c, err)
			return nil, p, false
		}
		return st, p, true
	}
	reply := func(c *gin.Context, st *pods.Store, slug string) {
		p, err := st.Get(c.Request.Context(), slug)
		if err != nil {
			podError(c, err)
			return
		}
		out := podOut(p)
		if missing, err := st.Readiness(c.Request.Context(), p); err == nil {
			out.Missing = missing
		}
		if w, err := st.Waivers(c.Request.Context(), p.ID); err == nil && len(w) > 0 {
			out.Waivers = w
		}
		c.JSON(http.StatusOK, gin.H{"pod": out})
	}

	s.GET("/pods", func(c *gin.Context) {
		st, v, ok := store(c)
		if !ok {
			return
		}
		list, err := st.ListFor(c.Request.Context(), v)
		if err != nil {
			podError(c, err)
			return
		}
		out := make([]podJSON, 0, len(list))
		for _, p := range list {
			out = append(out, podOut(p))
		}
		// Open alerts per pod, for the list's "needs attention" marks.
		alerts, _ := st.OpenAlertCounts(c.Request.Context())
		if alerts == nil {
			alerts = map[string]int{}
		}
		c.JSON(http.StatusOK, gin.H{"pods": out, "alerts": alerts})
	})
	s.POST("/pods", func(c *gin.Context) {
		st, v, ok := store(c)
		if !ok {
			return
		}
		if !v.Owner {
			c.JSON(http.StatusForbidden, gin.H{"error": "only the TFG owner creates client pods"})
			return
		}
		var in struct {
			ClientName string `json:"clientName"`
			Label      string `json:"label"`
			Package    string `json:"package"`
			Reps       int    `json:"reps"`
		}
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "clientName, package and reps expected"})
			return
		}
		p, err := st.Create(c.Request.Context(), pods.NewPod{ClientName: in.ClientName, Label: in.Label, OwnerID: v.UserID, Package: in.Package, Reps: in.Reps})
		if err != nil {
			podError(c, err)
			return
		}
		c.JSON(http.StatusCreated, gin.H{"pod": podOut(p)})
	})
	s.GET("/pods/:slug", func(c *gin.Context) {
		if st, p, ok := pod(c); ok {
			reply(c, st, p.Slug)
		}
	})
	s.POST("/pods/:slug/status", func(c *gin.Context) {
		st, p, ok := pod(c)
		if !ok {
			return
		}
		var in struct {
			Status string `json:"status"`
			Reason string `json:"reason"`
		}
		if err := c.ShouldBindJSON(&in); err != nil || in.Status == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "status expected"})
			return
		}
		if _, err := st.SetStatus(c.Request.Context(), p.Slug, in.Status, in.Reason); err != nil {
			podError(c, err)
			return
		}
		if warn := resyncPodAgent(c.Request.Context(), d, st, p.Slug); warn != "" {
			c.Header("X-Pod-Warning", warn)
		}
		reply(c, st, p.Slug)
	})
	s.PUT("/pods/:slug/sender", func(c *gin.Context) {
		st, p, ok := pod(c)
		if !ok {
			return
		}
		if who, _ := personOf(c); !who.Owner {
			c.JSON(http.StatusForbidden, gin.H{"error": "only the TFG owner changes a client's sender"})
			return
		}
		var in struct {
			Name, Email, Domain, AccountID string
		}
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "name and email expected"})
			return
		}
		if err := st.SetSender(c.Request.Context(), p.Slug, pods.Sender{Name: in.Name, Email: in.Email, Domain: in.Domain, AccountID: in.AccountID}); err != nil {
			podError(c, err)
			return
		}
		reply(c, st, p.Slug)
	})
	s.PUT("/pods/:slug/settings", func(c *gin.Context) {
		st, p, ok := pod(c)
		if !ok {
			return
		}
		if who, _ := personOf(c); !who.Owner {
			c.JSON(http.StatusForbidden, gin.H{"error": "only the TFG owner changes a client's sending rules"})
			return
		}
		var in struct {
			ApprovalPolicy     string `json:"approvalPolicy"`
			FirstEmailDailyCap int    `json:"firstEmailDailyCap"`
			FollowUpDailyCap   int    `json:"followUpDailyCap"`
			Timezone           string `json:"timezone"`
			SendWindow         [2]int `json:"sendWindow"`
			SendWeekends       bool   `json:"sendWeekends"`
			DryRunInbox        string `json:"dryRunInbox"`
			BookingURL         string `json:"bookingUrl"`
			CRMProvider        string `json:"crmProvider"`
			CalendarProvider   string `json:"calendarProvider"`
			HandoffEmail       string `json:"handoffEmail"`
			InboundAutoReply   bool   `json:"inboundAutoReply"`
		}
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "settings expected"})
			return
		}
		if err := st.UpdateSettings(c.Request.Context(), p.Slug, pods.Settings{ApprovalPolicy: in.ApprovalPolicy,
			FirstEmailDailyCap: in.FirstEmailDailyCap, FollowUpDailyCap: in.FollowUpDailyCap, Timezone: in.Timezone,
			SendWindowStart: in.SendWindow[0], SendWindowEnd: in.SendWindow[1], SendWeekends: in.SendWeekends,
			DryRunInbox: in.DryRunInbox, BookingURL: in.BookingURL, CRMProvider: in.CRMProvider, CalendarProvider: in.CalendarProvider,
			HandoffEmail: in.HandoffEmail, InboundAutoReply: in.InboundAutoReply}); err != nil {
			podError(c, err)
			return
		}
		if warn := resyncPodAgent(c.Request.Context(), d, st, p.Slug); warn != "" {
			c.Header("X-Pod-Warning", warn)
		}
		reply(c, st, p.Slug)
	})
	s.GET("/pods/:slug/docs/:key", func(c *gin.Context) {
		st, p, ok := pod(c)
		if !ok {
			return
		}
		doc, err := st.Doc(c.Request.Context(), p.Slug, c.Param("key"))
		if err != nil {
			podError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"doc": gin.H{"key": doc.Key, "body": doc.Body, "version": doc.Version, "approvedAt": doc.ApprovedAt, "updatedAt": doc.UpdatedAt}})
	})
	s.PUT("/pods/:slug/docs/:key", func(c *gin.Context) {
		st, p, ok := pod(c)
		if !ok {
			return
		}
		var in struct {
			Body string `json:"body"`
		}
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "body expected"})
			return
		}
		if err := st.SetDoc(c.Request.Context(), p.Slug, c.Param("key"), in.Body); err != nil {
			podError(c, err)
			return
		}
		reply(c, st, p.Slug)
	})
	// Records a client's sign-off given outside the portal (e.g. by email); the
	// portal records its own approvals with the client's identity.
	s.POST("/pods/:slug/docs/:key/approve", func(c *gin.Context) {
		st, p, ok := pod(c)
		if !ok {
			return
		}
		var in struct {
			By string `json:"by"`
		}
		if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.By) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "say who approved it and how (by)"})
			return
		}
		if err := st.ApproveDoc(c.Request.Context(), p.Slug, c.Param("key"), strings.TrimSpace(in.By)); err != nil {
			podError(c, err)
			return
		}
		if warn := resyncPodAgent(c.Request.Context(), d, st, p.Slug); warn != "" {
			c.Header("X-Pod-Warning", warn)
		}
		reply(c, st, p.Slug)
	})
	// The pod's own mailbox: owner-only, saved as POD_<SLUG>_* keys in the
	// protected env file, never echoed. Saving binds the pod's sender to it.
	s.GET("/pods/:slug/mailbox", func(c *gin.Context) {
		_, p, ok := pod(c)
		if !ok {
			return
		}
		get := func(k string) string { return d.Resolver.Resolve(email.PodKeyPrefix(p.Slug) + k) }
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{"mailbox": gin.H{
			"configured": get("HOST") != "" && get("USER") != "" && get("PASS") != "",
			"host":       get("HOST"), "user": get("USER"), "from": get("FROM"), "fromName": get("FROM_NAME"),
			"smtpHost": get("SMTP_HOST"), "smtpPort": get("SMTP_PORT"),
			"pass": integrations.MaskSecret(get("PASS")),
		}})
	})
	s.PUT("/pods/:slug/mailbox", func(c *gin.Context) {
		st, p, ok := pod(c)
		if !ok {
			return
		}
		if who, _ := personOf(c); !who.Owner {
			c.JSON(http.StatusForbidden, gin.H{"error": "only the TFG owner sets a client mailbox"})
			return
		}
		var in struct {
			Host, User, Pass, From, FromName, SMTPHost, SMTPPort, SMTPUser, SMTPPass string
		}
		if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.Host) == "" || strings.TrimSpace(in.User) == "" || strings.TrimSpace(in.Pass) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "host, user and pass are required"})
			return
		}
		values := map[string]string{}
		for k, v := range map[string]string{"HOST": in.Host, "USER": in.User, "PASS": in.Pass, "FROM": in.From,
			"FROM_NAME": in.FromName, "SMTP_HOST": in.SMTPHost, "SMTP_PORT": in.SMTPPort, "SMTP_USER": in.SMTPUser, "SMTP_PASS": in.SMTPPass} {
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			if !integrationsSafeValue(v) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "unsafe value for " + strings.ToLower(k)})
				return
			}
			values[email.PodKeyPrefix(p.Slug)+k] = v
		}
		path, ok := integrationsEnvLocal(c, d)
		if !ok {
			return
		}
		if err := integrations.UpsertEnv(path, values); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "could not write ~/.founderos/.env"})
			return
		}
		from := strings.TrimSpace(in.From)
		if from == "" {
			from = strings.TrimSpace(in.User)
		}
		if err := st.SetSender(c.Request.Context(), p.Slug, pods.Sender{Name: in.FromName, Email: from,
			AccountID: email.PodAccountPrefix + p.Slug}); err != nil {
			podError(c, err)
			return
		}
		_ = st.Log(c.Request.Context(), p.ID, "mailbox", "Sending mailbox set to "+from, "")
		c.Header("Cache-Control", "no-store")
		reply(c, st, p.Slug)
	})
	// The pod's outbox. Every send, a click included, goes through the pod gate.
	s.GET("/pods/:slug/outbox", func(c *gin.Context) {
		_, p, ok := pod(c)
		if !ok {
			return
		}
		page, err := outbox.Load(c.Request.Context(), d.Pool, p.Slug)
		if err != nil {
			podError(c, err)
			return
		}
		c.JSON(http.StatusOK, page)
	})
	s.POST("/pods/:slug/outbox/:id/approve", func(c *gin.Context) {
		_, p, ok := pod(c)
		if !ok {
			return
		}
		var in outbox.Content
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "subject and body expected"})
			return
		}
		it, err := outbox.Approve(c.Request.Context(), d.Pool, p.Slug, c.Param("id"), in, outboxSender(d))
		switch {
		case err == nil:
			c.JSON(http.StatusOK, gin.H{"ok": true, "item": it})
		case errors.Is(err, outbox.ErrNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		case errors.Is(err, outbox.ErrBadContent):
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		case errors.Is(err, outbox.ErrHeld), errors.Is(err, outbox.ErrAwaitingClient), errors.Is(err, outbox.ErrSuppressed),
			errors.Is(err, outbox.ErrForeignAccount), errors.Is(err, outbox.ErrConversationStarted), errors.Is(err, outbox.ErrNotPending):
			c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "item": it})
		default:
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error(), "item": it})
		}
	})
	s.POST("/pods/:slug/outbox/:id/reject", func(c *gin.Context) {
		_, p, ok := pod(c)
		if !ok {
			return
		}
		it, err := outbox.Reject(c.Request.Context(), d.Pool, p.Slug, c.Param("id"))
		switch {
		case err == nil:
			c.JSON(http.StatusOK, gin.H{"ok": true, "item": it})
		case errors.Is(err, outbox.ErrNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		case errors.Is(err, outbox.ErrNotPending):
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		default:
			podError(c, err)
		}
	})
	s.GET("/pods/:slug/agent", func(c *gin.Context) {
		st, p, ok := pod(c)
		if !ok {
			return
		}
		bind, found, err := st.GetBinding(c.Request.Context(), p.ID)
		if err != nil {
			podError(c, err)
			return
		}
		if !found {
			c.JSON(http.StatusOK, gin.H{"agent": nil})
			return
		}
		c.JSON(http.StatusOK, gin.H{"agent": gin.H{"projectId": bind.ProjectID, "agentId": bind.AgentID,
			"researchRoutineId": bind.ResearchRoutineID, "outreachRoutineId": bind.OutreachRoutineID, "routinesActive": bind.RoutinesActive}})
	})
	// Provision (or refresh) the pod's AI SDR on the task board. Owner only.
	s.POST("/pods/:slug/agent", func(c *gin.Context) {
		st, p, ok := pod(c)
		if !ok {
			return
		}
		if who, _ := personOf(c); !who.Owner {
			c.JSON(http.StatusForbidden, gin.H{"error": "only the TFG owner provisions a pod's AI SDR"})
			return
		}
		bind, err := st.Provision(c.Request.Context(), podBoardFor(d), p.Slug)
		if err != nil {
			podError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"agent": gin.H{"projectId": bind.ProjectID, "agentId": bind.AgentID,
			"researchRoutineId": bind.ResearchRoutineID, "outreachRoutineId": bind.OutreachRoutineID, "routinesActive": bind.RoutinesActive}})
	})
	// Sets the client up in the portal: creates their portal record (Starter/Core/The Full, reps)
	// unless the pod is already linked, then links the pod. Inviting people is done in the staff CRM.
	s.POST("/pods/:slug/portal/setup", func(c *gin.Context) {
		st, p, ok := pod(c)
		if !ok {
			return
		}
		if who, _ := personOf(c); !who.Owner {
			c.JSON(http.StatusForbidden, gin.H{"error": "only the TFG owner sets up a client portal"})
			return
		}
		rest, ok := portalsync.NewREST(func(k string) string { return d.Resolver.Resolve(k) })
		if !ok {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "The client portal isn't configured on this OS (PORTAL_SUPABASE_URL and key)."})
			return
		}
		if p.PortalClientID != nil && *p.PortalClientID != "" {
			reply(c, st, p.Slug)
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
		defer cancel()
		pkg := map[string]string{"custom": "Starter", "starter": "Starter", "core": "Core", "full": "The Full"}[strings.ToLower(p.Package)]
		if pkg == "" {
			pkg = p.Package
		}
		id, err := rest.CreateClient(ctx, p.ClientName, pkg, p.Reps)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "The portal couldn't create the client: " + err.Error()})
			return
		}
		if _, err := portalsync.Relink(ctx, st, rest, p.Slug, id); err != nil {
			podError(c, err)
			return
		}
		_ = st.Log(ctx, p.ID, "portal_setup", "Client created in the portal and linked; invite their people from the staff CRM", id)
		reply(c, st, p.Slug)
	})
	// Who can sign in to this client's portal, so access is verified, not assumed.
	s.GET("/pods/:slug/portal/access", func(c *gin.Context) {
		_, p, ok := pod(c)
		if !ok {
			return
		}
		if p.PortalClientID == nil || *p.PortalClientID == "" {
			c.JSON(http.StatusOK, gin.H{"linked": false, "people": []string{}})
			return
		}
		rest, ok := portalsync.NewREST(func(k string) string { return d.Resolver.Resolve(k) })
		if !ok {
			c.JSON(http.StatusOK, gin.H{"linked": true, "known": false, "people": []string{}, "error": "portal not configured on this OS"})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
		defer cancel()
		people, err := rest.Access(ctx, *p.PortalClientID)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"linked": true, "known": false, "people": []string{}, "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"linked": true, "known": true, "people": people,
			"crmUrl": "https://thefoundersgroup.net/app/crm.html#client/" + *p.PortalClientID})
	})
	s.PUT("/pods/:slug/portal", func(c *gin.Context) {
		st, p, ok := pod(c)
		if !ok {
			return
		}
		if who, _ := personOf(c); !who.Owner {
			c.JSON(http.StatusForbidden, gin.H{"error": "only the TFG owner links a client portal"})
			return
		}
		var in struct {
			PortalClientID string `json:"portalClientId"`
		}
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "portalClientId expected"})
			return
		}
		// With the portal reachable, the client must exist there and a move to another client takes the
		// pod's rows with it; without portal settings the id is only checked for shape.
		if rest, ok := portalsync.NewREST(func(k string) string { return d.Resolver.Resolve(k) }); ok {
			ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
			defer cancel()
			if _, err := portalsync.Relink(ctx, st, rest, p.Slug, in.PortalClientID); err != nil {
				if errors.Is(err, portalsync.ErrNoSuchClient) {
					c.JSON(http.StatusBadRequest, gin.H{"error": "The client portal has no client with that id. Copy it from the client's page in the staff CRM."})
					return
				}
				podError(c, err)
				return
			}
		} else if err := st.LinkPortal(c.Request.Context(), p.Slug, in.PortalClientID); err != nil {
			podError(c, err)
			return
		}
		reply(c, st, p.Slug)
	})
	// A service item this client genuinely doesn't need (e.g. no portal, no calendar), waived with a reason.
	s.POST("/pods/:slug/readiness-waivers", func(c *gin.Context) {
		st, p, ok := pod(c)
		if !ok {
			return
		}
		who, _ := personOf(c)
		if !who.Owner {
			c.JSON(http.StatusForbidden, gin.H{"error": "only the TFG owner waives a go-live requirement"})
			return
		}
		var in struct {
			Item   string `json:"item"`
			Reason string `json:"reason"`
			Waive  bool   `json:"waive"`
		}
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "item, reason and waive expected"})
			return
		}
		by := who.Name
		if by == "" {
			by = "the owner"
		}
		if err := st.WaiveReadiness(c.Request.Context(), p.Slug, in.Item, in.Reason, by, in.Waive); err != nil {
			podError(c, err)
			return
		}
		reply(c, st, p.Slug)
	})
	s.POST("/pods/:slug/target-lists", func(c *gin.Context) {
		st, p, ok := pod(c)
		if !ok {
			return
		}
		var in struct {
			Title   string   `json:"title"`
			Domains []string `json:"domains"`
		}
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "title and domains expected"})
			return
		}
		if err := st.ProposeTargetList(c.Request.Context(), p.Slug, in.Title, in.Domains); err != nil {
			podError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	s.GET("/pods/:slug/approvals", func(c *gin.Context) {
		st, p, ok := pod(c)
		if !ok {
			return
		}
		list, err := st.Approvals(c.Request.Context(), p.ID)
		if err != nil {
			podError(c, err)
			return
		}
		out := make([]gin.H, 0, len(list))
		for _, a := range list {
			out = append(out, gin.H{"kind": a.Kind, "ref": a.Ref, "title": a.Title, "status": a.Status})
		}
		c.JSON(http.StatusOK, gin.H{"approvals": out})
	})
	// A new private form link for the client's website (replaces the old one). Owner only.
	s.POST("/pods/:slug/inbound-link", func(c *gin.Context) {
		st, p, ok := pod(c)
		if !ok {
			return
		}
		if who, _ := personOf(c); !who.Owner {
			c.JSON(http.StatusForbidden, gin.H{"error": "only the TFG owner makes a client's form link"})
			return
		}
		token, err := st.NewInboundToken(c.Request.Context(), p.Slug)
		if err != nil {
			podError(c, err)
			return
		}
		endpoint := inboundBase() + "/api/founderos/inbound/" + token
		_ = st.Log(c.Request.Context(), p.ID, "settings", "New website form link made", "")
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{"endpoint": endpoint, "snippet": InboundSnippet(endpoint)})
	})
	// The client's own Cal.com account (owner only): bookings there become
	// meetings with call briefs. The key is saved like the mailbox password.
	s.PUT("/pods/:slug/calendar", func(c *gin.Context) {
		st, p, ok := pod(c)
		if !ok {
			return
		}
		if who, _ := personOf(c); !who.Owner {
			c.JSON(http.StatusForbidden, gin.H{"error": "only the TFG owner connects a client's calendar"})
			return
		}
		var in struct {
			CalcomKey string `json:"calcomKey"`
		}
		if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.CalcomKey) == "" || !integrationsSafeValue(strings.TrimSpace(in.CalcomKey)) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "calcomKey expected"})
			return
		}
		path, ok := integrationsEnvLocal(c, d)
		if !ok {
			return
		}
		if err := integrations.UpsertEnv(path, map[string]string{caldotcom.PodKeyName(email.PodKeyPrefix(p.Slug)): strings.TrimSpace(in.CalcomKey)}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "could not write ~/.founderos/.env"})
			return
		}
		if _, err := d.Pool.Exec(c.Request.Context(), `UPDATE founderos_client_pods SET calendar_provider = 'calcom', updated_at = now() WHERE slug = $1`, p.Slug); err != nil {
			podError(c, err)
			return
		}
		_ = st.Log(c.Request.Context(), p.ID, "settings", "Cal.com calendar connected", "")
		c.Header("Cache-Control", "no-store")
		reply(c, st, p.Slug)
	})
	s.GET("/pods/:slug/meetings", func(c *gin.Context) {
		_, p, ok := pod(c)
		if !ok {
			return
		}
		rows, err := d.Pool.Query(c.Request.Context(), `SELECT id, booking_uid, title, attendee_name, attendee_email, starts_at, brief,
			brief_sent_at IS NOT NULL, status, outcome, transcript <> '', coaching, coaching_sent_at IS NOT NULL,
			brief_delivery, coaching_delivery
			FROM founderos_pod_meetings WHERE pod_id = $1 ORDER BY starts_at DESC NULLS LAST LIMIT 100`, p.ID)
		if err != nil {
			podError(c, err)
			return
		}
		defer rows.Close()
		out := []gin.H{}
		for rows.Next() {
			var id int64
			var uid, title, name, mail, brief, status, outcome, coaching string
			var at *time.Time
			var sent, hasTranscript, coachingSent bool
			var briefDelivery, coachingDelivery string
			if err := rows.Scan(&id, &uid, &title, &name, &mail, &at, &brief, &sent, &status, &outcome, &hasTranscript, &coaching, &coachingSent,
				&briefDelivery, &coachingDelivery); err != nil {
				podError(c, err)
				return
			}
			out = append(out, gin.H{"id": id, "uid": uid, "title": title, "name": name, "email": mail, "startsAt": at, "brief": brief,
				"briefSent": sent, "status": status, "outcome": outcome, "hasTranscript": hasTranscript, "coaching": coaching, "coachingSent": coachingSent,
				"briefDelivery": briefDelivery, "coachingDelivery": coachingDelivery})
		}
		c.JSON(http.StatusOK, gin.H{"meetings": out, "calendarConnected": podCalendarKey(d, p.Slug) != "",
			"recorderConnected": podRecorderKey(d, p.Slug) != ""})
	})
	// How a call went, by hand: held (with an optional pasted transcript from
	// any recorder, e.g. Plaud) or no-show. A transcript gets coaching notes.
	s.PUT("/pods/:slug/meetings/:id/outcome", func(c *gin.Context) {
		st, p, ok := pod(c)
		if !ok {
			return
		}
		id, err := strconv.ParseInt(c.Param("id"), 10, 64)
		var in struct {
			Outcome    string `json:"outcome"`
			Transcript string `json:"transcript"`
		}
		if err != nil || c.ShouldBindJSON(&in) != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "outcome expected"})
			return
		}
		if err := st.SetCallOutcome(c.Request.Context(), p.Slug, id, in.Outcome, in.Transcript); err != nil {
			podError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	// The client's own Fathom (owner only): recordings of booked calls are
	// matched to them and get coaching notes. Saved like the calendar key.
	s.PUT("/pods/:slug/recorder", func(c *gin.Context) {
		st, p, ok := pod(c)
		if !ok {
			return
		}
		if who, _ := personOf(c); !who.Owner {
			c.JSON(http.StatusForbidden, gin.H{"error": "only the TFG owner connects a client's call recorder"})
			return
		}
		var in struct {
			FathomKey string `json:"fathomKey"`
		}
		if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.FathomKey) == "" || !integrationsSafeValue(strings.TrimSpace(in.FathomKey)) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "fathomKey expected"})
			return
		}
		path, ok := integrationsEnvLocal(c, d)
		if !ok {
			return
		}
		if err := integrations.UpsertEnv(path, map[string]string{fathomcalls.PodKeyName(email.PodKeyPrefix(p.Slug)): strings.TrimSpace(in.FathomKey)}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "could not write ~/.founderos/.env"})
			return
		}
		_ = st.Log(c.Request.Context(), p.ID, "settings", "Fathom call recorder connected", "")
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	// Where the client started (month one), to measure results against.
	s.PUT("/pods/:slug/baseline", func(c *gin.Context) {
		st, p, ok := pod(c)
		if !ok {
			return
		}
		var in pods.Baseline
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "baseline expected"})
			return
		}
		if err := st.SetBaseline(c.Request.Context(), p.Slug, in); err != nil {
			podError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	// Weekly results and reviews, newest first, with the baseline and this
	// week so far.
	s.GET("/pods/:slug/weekly", func(c *gin.Context) {
		st, p, ok := pod(c)
		if !ok {
			return
		}
		ctx := c.Request.Context()
		weeks, err := st.Weeks(ctx, p.ID, 26)
		if err != nil {
			podError(c, err)
			return
		}
		base, _ := st.GetBaseline(ctx, p.ID)
		from := pods.WeekOf(time.Now(), p.Timezone)
		now, err := st.WeeklyMetrics(ctx, p, from, from.AddDate(0, 0, 7))
		if err != nil {
			podError(c, err)
			return
		}
		out := make([]gin.H, 0, len(weeks))
		for _, w := range weeks {
			out = append(out, gin.H{"week": w.Start, "metrics": w.M, "review": w.Review, "corrected": w.Corrected, "delivery": w.Delivery})
		}
		c.JSON(http.StatusOK, gin.H{"weeks": out, "baseline": base, "thisWeek": now})
	})
	// Open alerts: what about this AI SDR needs a person right now.
	s.GET("/pods/:slug/alerts", func(c *gin.Context) {
		st, p, ok := pod(c)
		if !ok {
			return
		}
		list, err := st.Alerts(c.Request.Context(), p.ID)
		if err != nil {
			podError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"alerts": list})
	})
	// Refresh this pod's alerts now (after fixing something), without waiting
	// for the five-minute check.
	s.POST("/pods/:slug/alerts/refresh", func(c *gin.Context) {
		st, p, ok := pod(c)
		if !ok {
			return
		}
		pushPodAlerts(c.Request.Context(), st)
		list, err := st.Alerts(c.Request.Context(), p.ID)
		if err != nil {
			podError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"alerts": list})
	})
	// A person dealt with a stuck AI task: stop flagging it.
	s.POST("/pods/:slug/tasks/:issue/resolved", func(c *gin.Context) {
		st, p, ok := pod(c)
		if !ok {
			return
		}
		ctx := c.Request.Context()
		if err := st.ResolveStuck(ctx, p.Slug, c.Param("issue")); err != nil {
			podError(c, err)
			return
		}
		_ = st.Log(ctx, p.ID, "task_watch", "A stuck AI task was marked dealt with", c.Param("issue"))
		pushPodAlerts(ctx, st)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	// Hand-off bundle (owner only): everything the client owns, as one zip.
	s.GET("/pods/:slug/handoff-bundle", func(c *gin.Context) {
		st, p, ok := pod(c)
		if !ok {
			return
		}
		if who, _ := personOf(c); !who.Owner {
			c.JSON(http.StatusForbidden, gin.H{"error": "only the TFG owner exports a client's hand-off bundle"})
			return
		}
		raw, err := st.Bundle(c.Request.Context(), p.Slug)
		if err != nil {
			podError(c, err)
			return
		}
		_ = st.Log(c.Request.Context(), p.ID, "handoff_bundle", "Hand-off bundle exported", "")
		name := strings.TrimPrefix(p.Slug, "client-") + "-ai-sdr-handoff-" + time.Now().Format("2006-01-02") + ".zip"
		c.Header("Cache-Control", "no-store")
		c.Header("Content-Disposition", `attachment; filename="`+name+`"`)
		c.Data(http.StatusOK, "application/zip", raw)
	})
	registerPodCRM(s, d, pod, reply)
	s.GET("/pods/:slug/activity", func(c *gin.Context) {
		st, p, ok := pod(c)
		if !ok {
			return
		}
		list, err := st.Recent(c.Request.Context(), p.ID, 100)
		if err != nil {
			podError(c, err)
			return
		}
		out := make([]gin.H, 0, len(list))
		for _, a := range list {
			out = append(out, gin.H{"at": a.At, "kind": a.Kind, "summary": a.Summary, "ref": a.Ref})
		}
		c.JSON(http.StatusOK, gin.H{"activity": out})
	})
}

func podError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, pods.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "no such client pod"})
	case errors.Is(err, pods.ErrExists):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case errors.Is(err, pods.ErrTransition), errors.Is(err, pods.ErrNotReady):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case errors.Is(err, pods.ErrInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	default:
		slog.Error("client pods", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "client pods: internal error"})
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
