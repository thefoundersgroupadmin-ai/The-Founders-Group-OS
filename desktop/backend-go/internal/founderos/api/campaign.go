package api

// The pre-approved outreach campaign (FOUNDEROS_CAMPAIGN=<task identifier>):
// its first emails go out without a click, one per five-minute pass, inside
// US business hours, up to FOUNDEROS_CAMPAIGN_DAILY_CAP (20) a day and
// FOUNDEROS_CAMPAIGN_TOTAL (50) in all. Two failed sends in a row stop it
// (~/.founderos/campaign-halted) and tell the phone; nothing restarts it but
// deleting that file.

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rhl/businessos-backend/internal/founderos/notify"
	"github.com/rhl/businessos-backend/internal/founderos/pages/outbox"
	"github.com/rhl/businessos-backend/internal/founderos/people"
)

const ownDomain = "thefoundersgroup.net"

func envInt(name string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name))); err == nil && n >= 0 {
		return n
	}
	return def
}

func campaignHaltPath() string { return filepath.Join(filepath.Dir(people.Root()), "campaign-halted") }

// CampaignHalted says why the campaign is stopped, or "".
func campaignHalted() string {
	raw, err := os.ReadFile(campaignHaltPath())
	if err != nil {
		return ""
	}
	if s := strings.TrimSpace(string(raw)); s != "" {
		return s
	}
	return "stopped"
}

var campaignState struct {
	sync.Mutex
	failuresInARow int
	heldSeen       map[string]bool
}

// campaignAutoSend is swapped in tests.
var campaignAutoSend func(ctx context.Context, c outbox.Campaign, now time.Time) (outbox.CampaignResult, error)

func sendCampaign(ctx context.Context, d *Deps, now time.Time) {
	autoSend := campaignAutoSend
	if autoSend == nil {
		if d.Pool == nil {
			return
		}
		autoSend = func(ctx context.Context, c outbox.Campaign, now time.Time) (outbox.CampaignResult, error) {
			return outbox.AutoSendCampaign(ctx, d.Pool, outboxWorkspace, c, ownDomain, now, outboxSender(d))
		}
	}
	c := outbox.Campaign{
		Ref:      strings.TrimSpace(os.Getenv("FOUNDEROS_CAMPAIGN")),
		DailyCap: envInt("FOUNDEROS_CAMPAIGN_DAILY_CAP", 20),
		TotalCap: envInt("FOUNDEROS_CAMPAIGN_TOTAL", 50),
		Window:   outbox.USBusinessHours,
	}
	if c.Ref == "" {
		return
	}
	if why := campaignHalted(); why != "" {
		return
	}
	res, err := autoSend(ctx, c, now)

	campaignState.Lock()
	defer campaignState.Unlock()
	if campaignState.heldSeen == nil {
		campaignState.heldSeen = map[string]bool{}
	}
	for id, why := range res.Held {
		if !campaignState.heldSeen[id] {
			campaignState.heldSeen[id] = true
			slog.Info("founderos campaign: held for a look", "id", id, "why", why)
		}
	}
	switch {
	case res.Failures > 0:
		campaignState.failuresInARow++
		slog.Warn("founderos campaign: send failed", "campaign", c.Ref, "err", err, "inARow", campaignState.failuresInARow)
	case res.Sent > 0:
		campaignState.failuresInARow = 0
		slog.Info("founderos campaign: sent without a click", "campaign", c.Ref)
	}
	if campaignState.failuresInARow >= 2 {
		why := fmt.Sprintf("%s stopped at %s after two failed sends in a row: %v", c.Ref, now.Format(time.RFC3339), err)
		_ = os.WriteFile(campaignHaltPath(), []byte(why+"\n"), 0o600)
		campaignState.failuresInARow = 0
		notify.Publish(ctx, notify.Event{ID: "campaign-halt-" + c.Ref + "-" + now.Format("20060102T1504"), Kind: notify.KindHealth,
			Title: "Outreach stopped", Body: notifyClip(why, 180), URL: "/os/outbox"})
	}
}
