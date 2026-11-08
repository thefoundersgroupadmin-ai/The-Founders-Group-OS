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
	CRMProvider        string     `json:"crmProvider"`
	CalendarProvider   string     `json:"calendarProvider"`
	BookingURL         string     `json:"bookingUrl"`
	PausedReason       string     `json:"pausedReason"`
	WentLiveAt         *time.Time `json:"wentLiveAt"`
	Missing            []string   `json:"missingToGoLive,omitempty"`
}

func podOut(p pods.Pod) podJSON {
	return podJSON{Slug: p.Slug, ClientName: p.ClientName, Package: p.Package, Reps: p.Reps, Status: p.Status,
		SenderName: p.SenderName, SenderEmail: p.SenderEmail, SendingDomain: p.SendingDomain,
		ApprovalPolicy: p.ApprovalPolicy, FirstEmailDailyCap: p.FirstEmailDailyCap, FollowUpDailyCap: p.FollowUpDailyCap,
		Timezone: p.Timezone, SendWindow: [2]int{p.SendWindowStart, p.SendWindowEnd}, SendWeekends: p.SendWeekends,
		CRMProvider: p.CRMProvider, CalendarProvider: p.CalendarProvider, BookingURL: p.BookingURL,
		PausedReason: p.PausedReason, WentLiveAt: p.WentLiveAt}
}

func registerPods(s *gin.RouterGroup, d *Deps) {
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
		reply(c, st, p.Slug)
	})
	s.PUT("/pods/:slug/sender", func(c *gin.Context) {
		st, p, ok := pod(c)
		if !ok {
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
		reply(c, st, p.Slug)
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
