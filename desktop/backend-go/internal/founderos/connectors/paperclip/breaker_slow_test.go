package paperclip

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 2 Nov 2026: with all 39 agents running, Paperclip sometimes answered a list
// after the time limit. That opened the 30s breaker, and every OS page said
// "Board unreachable" although the board was up, just slow. A slow answer is
// that one call failing; only a real network failure opens the breaker.
func TestASlowBoardDoesNotOpenTheBreaker(t *testing.T) {
	slow := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if slow {
			time.Sleep(200 * time.Millisecond)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	c := New(resolver(t, "PAPERCLIP_API_URL="+srv.URL, "PAPERCLIP_BOARD_KEY=k", "PAPERCLIP_COMPANY_ID=c1"))
	ctx := context.Background()
	if _, err := c.get(ctx, "/api/x", 20*time.Millisecond); err == nil {
		t.Fatal("the slow call itself should fail")
	}
	slow = false
	if _, err := c.get(ctx, "/api/x", time.Second); err != nil {
		t.Fatalf("the next call must reach the board, not the breaker: %v", err)
	}
}

func TestADeadBoardStillOpensTheBreaker(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // connection refused
	c := New(resolver(t, "PAPERCLIP_API_URL="+url, "PAPERCLIP_BOARD_KEY=k", "PAPERCLIP_COMPANY_ID=c1"))
	ctx := context.Background()
	c.get(ctx, "/api/x", time.Second)
	if _, err := c.get(ctx, "/api/x", time.Second); err == nil || !strings.Contains(err.Error(), "not re-probed") {
		t.Fatalf("a refused connection should open the breaker: %v", err)
	}
}

func TestListsWaitLongEnoughForABusyBoard(t *testing.T) {
	if listTimeout < 8*time.Second {
		t.Fatalf("listTimeout %v: a busy board takes ~2.5s on its slowest list", listTimeout)
	}
}
