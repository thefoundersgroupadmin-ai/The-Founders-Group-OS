package api

// /api/founderos/pods: client AI SDR pods (docs/tfg/CLIENT-PODS.md). Only the
// installation owner creates pods; everyone else reaches a pod only as a
// member of its workspace, and a pod they may not see answers 404.

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rhl/businessos-backend/internal/founderos/connectors/email"
	"github.com/rhl/businessos-backend/internal/founderos/pages/integrations"
	"github.com/rhl/businessos-backend/internal/founderos/pages/outbox"
	"github.com/rhl/businessos-backend/internal/founderos/pages/pods"
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
	WentLiveAt         *time.Time `json:"wentLiveAt"`
	Missing            []string   `json:"missingToGoLive,omitempty"`
}

func podOut(p pods.Pod) podJSON {
	return podJSON{Slug: p.Slug, ClientName: p.ClientName, Package: p.Package, Reps: p.Reps, Status: p.Status,
		SenderName: p.SenderName, SenderEmail: p.SenderEmail, SendingDomain: p.SendingDomain,
		ApprovalPolicy: p.ApprovalPolicy, FirstEmailDailyCap: p.FirstEmailDailyCap, FollowUpDailyCap: p.FollowUpDailyCap,
		Timezone: p.Timezone, SendWindow: [2]int{p.SendWindowStart, p.SendWindowEnd}, SendWeekends: p.SendWeekends, DryRunInbox: p.DryRunInbox,
		CRMProvider: p.CRMProvider, CalendarProvider: p.CalendarProvider, BookingURL: p.BookingURL,
		PausedReason: p.PausedReason, WentLiveAt: p.WentLiveAt, PortalClientID: deref(p.PortalClientID)}
}

func registerPods(s *gin.RouterGroup, d *Deps) {
	startPortalSync(d)
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
		c.JSON(http.StatusOK, gin.H{"pods": out})
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
			Package    string `json:"package"`
			Reps       int    `json:"reps"`
		}
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "clientName, package and reps expected"})
			return
		}
		p, err := st.Create(c.Request.Context(), pods.NewPod{ClientName: in.ClientName, OwnerID: v.UserID, Package: in.Package, Reps: in.Reps})
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
		}
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "settings expected"})
			return
		}
		if err := st.UpdateSettings(c.Request.Context(), p.Slug, pods.Settings{ApprovalPolicy: in.ApprovalPolicy,
			FirstEmailDailyCap: in.FirstEmailDailyCap, FollowUpDailyCap: in.FollowUpDailyCap, Timezone: in.Timezone,
			SendWindowStart: in.SendWindow[0], SendWindowEnd: in.SendWindow[1], SendWeekends: in.SendWeekends,
			DryRunInbox: in.DryRunInbox, BookingURL: in.BookingURL, CRMProvider: in.CRMProvider, CalendarProvider: in.CalendarProvider}); err != nil {
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
		if err := st.LinkPortal(c.Request.Context(), p.Slug, in.PortalClientID); err != nil {
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
