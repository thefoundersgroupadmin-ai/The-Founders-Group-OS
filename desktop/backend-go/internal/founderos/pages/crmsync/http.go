package crmsync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rhl/businessos-backend/internal/founderos/guard"
)

var client = &http.Client{Timeout: 20 * time.Second}

// call sends one JSON request; writes (anything but GET) go through the writes guard.
func call(ctx context.Context, method, url string, headers map[string]string, body, out any) error {
	do := func() error {
		var rd io.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			if err != nil {
				return err
			}
			rd = bytes.NewReader(raw)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, rd)
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		res, err := client.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.StatusCode >= 300 {
			msg, _ := io.ReadAll(io.LimitReader(res.Body, 300))
			return fmt.Errorf("%s %s: %d %s", method, strings.SplitN(url, "?", 2)[0], res.StatusCode, strings.TrimSpace(string(msg)))
		}
		if out != nil {
			return json.NewDecoder(res.Body).Decode(out)
		}
		return nil
	}
	if method == http.MethodGet {
		return do()
	}
	return guard.Outbound("crm.sync", do)
}

// callRead is a POST that only reads (HubSpot search): not a write, no guard.
func callRead(ctx context.Context, url string, headers map[string]string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		return fmt.Errorf("POST %s: %d %s", url, res.StatusCode, strings.TrimSpace(string(msg)))
	}
	return json.NewDecoder(res.Body).Decode(out)
}
