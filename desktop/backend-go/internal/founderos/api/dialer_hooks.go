package api

// Public callbacks (no session; each proves itself):
//   POST /hooks/twilio/voice   Twilio asks what to do with a browser call: the
//                              OS answers with <Dial> only for a call row it
//                              just made, for that caller, to that number.
//   POST /hooks/twilio/status  how the phone leg is going (ringing, answered…).
//   POST /hooks/apollo?k=…     the phone numbers Apollo revealed.
// Twilio requests are checked against X-Twilio-Signature with the account's
// auth token; Apollo's against the OS's own hook secret.

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rhl/businessos-backend/internal/founderos/connectors/apollo"
	"github.com/rhl/businessos-backend/internal/founderos/connectors/twilio"
	"github.com/rhl/businessos-backend/internal/founderos/pages/dialer"
)

// A call row older than this can't be dialled by the voice hook.
const dialWindow = 2 * time.Minute

func registerDialerHooks(b *gin.RouterGroup, d *Deps) {
	b.POST("/hooks/twilio/voice", func(c *gin.Context) {
		tw := twilioFor(d)
		if err := c.Request.ParseForm(); err != nil || !tw.Valid(hookBase()+hookVoicePath, c.Request.PostForm, c.GetHeader("X-Twilio-Signature")) {
			c.String(http.StatusForbidden, "bad signature")
			return
		}
		twiml := func(s string) { c.Data(http.StatusOK, "text/xml; charset=utf-8", []byte(s)) }
		if d.Pool == nil {
			twiml(twilio.RefuseTwiML("The dialer is offline."))
			return
		}
		ctx := c.Request.Context()
		call, err := dialer.Get(ctx, d.Pool, c.Request.PostForm.Get("CallId"))
		switch {
		case err != nil:
			twiml(twilio.RefuseTwiML("That call was not started from the dialer."))
			return
		case "client:"+callerIdentity(call.Caller) != c.Request.PostForm.Get("From"):
			twiml(twilio.RefuseTwiML("That call belongs to someone else."))
			return
		case call.Status != "dialing" || time.Since(call.StartedAt) > dialWindow:
			twiml(twilio.RefuseTwiML("That call has expired. Press call again."))
			return
		}
		_ = dialer.Track(ctx, d.Pool, call.ID, "", "initiated", -1, time.Now())
		twiml(twilio.DialTwiML(tw.CallerID(), call.To, hookBase()+hookStatusPath+"?call="+call.ID))
	})

	b.POST("/hooks/twilio/status", func(c *gin.Context) {
		if err := c.Request.ParseForm(); err != nil ||
			!twilioFor(d).Valid(hookBase()+hookStatusPath+"?"+c.Request.URL.RawQuery, c.Request.PostForm, c.GetHeader("X-Twilio-Signature")) {
			c.String(http.StatusForbidden, "bad signature")
			return
		}
		if d.Pool != nil {
			dur, err := strconv.Atoi(c.Request.PostForm.Get("CallDuration"))
			if err != nil {
				dur = -1
			}
			_ = dialer.Track(c.Request.Context(), d.Pool, c.Query("call"), c.Request.PostForm.Get("CallSid"), c.Request.PostForm.Get("CallStatus"), dur, time.Now())
		}
		c.Status(http.StatusNoContent)
	})

	b.POST("/hooks/apollo", func(c *gin.Context) {
		want := strings.TrimSpace(d.Resolver.Resolve("FOUNDEROS_HOOK_SECRET"))
		if want == "" || subtle.ConstantTimeCompare([]byte(c.Query("k")), []byte(want)) != 1 {
			c.String(http.StatusForbidden, "bad key")
			return
		}
		var body apollo.Webhook
		if err := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 2<<20)).Decode(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "not JSON"})
			return
		}
		if d.Pool != nil {
			applyApollo(c.Request.Context(), d, body)
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
}

func applyApollo(ctx context.Context, d *Deps, body apollo.Webhook) {
	for _, p := range body.People {
		best := apollo.Best(p.PhoneNumbers)
		if p.ID == "" || best == "" {
			continue
		}
		var contactID string
		if d.Pool.QueryRow(ctx, `SELECT contact_id FROM founderos_dial_state WHERE apollo_id = $1`, p.ID).Scan(&contactID) == nil {
			setFoundPhone(ctx, d, contactID, best)
		}
	}
}
