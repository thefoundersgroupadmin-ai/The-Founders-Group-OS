package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rhl/businessos-backend/internal/founderos/notify"
	"github.com/rhl/businessos-backend/internal/founderos/pages/outbox"
)

func TestTwoFailedSendsInARowStopTheCampaign(t *testing.T) {
	peopleSetup(t)
	restore := notify.SetPushForTest(func(context.Context, []byte, notify.Device) (int, error) { return 201, nil })
	defer restore()
	t.Setenv("FOUNDEROS_CAMPAIGN", "THE-30")
	calls := 0
	old := campaignAutoSend
	campaignAutoSend = func(context.Context, outbox.Campaign, time.Time) (outbox.CampaignResult, error) {
		calls++
		return outbox.CampaignResult{Failures: 1}, errors.New("smtp: 550")
	}
	defer func() { campaignAutoSend = old }()
	d := &Deps{}
	now := time.Now()
	sendCampaign(context.Background(), d, now)
	if campaignHalted() != "" {
		t.Fatal("one failure must not stop it")
	}
	sendCampaign(context.Background(), d, now)
	if campaignHalted() == "" {
		t.Fatal("two in a row must stop it")
	}
	sendCampaign(context.Background(), d, now)
	if calls != 2 {
		t.Fatalf("kept sending after the stop: %d", calls)
	}
}
