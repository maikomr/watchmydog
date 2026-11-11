package go2rtc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// client is a minimal typed wrapper over the parts of go2rtc's HTTP API the
// engine needs (https://github.com/AlexxIT/go2rtc#module-api).
type client struct {
	base *url.URL
	http *http.Client
}

func newClient(apiAddr string) *client {
	return &client{
		base: &url.URL{Scheme: "http", Host: apiAddr},
		http: &http.Client{Timeout: 5 * time.Second},
	}
}

// streamInfo mirrors the JSON of GET /api/streams.
type streamInfo struct {
	Producers []struct {
		URL    string `json:"url"`
		Medias []any  `json:"medias"`
	} `json:"producers"`
	Consumers []struct {
		Type string `json:"type"`
	} `json:"consumers"`
}

// streams returns all configured streams.
func (c *client) streams(ctx context.Context) (map[string]streamInfo, error) {
	var out map[string]streamInfo
	if err := c.getJSON(ctx, "/api/streams", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ping reports whether go2rtc answers its API.
func (c *client) ping(ctx context.Context) error {
	var out map[string]streamInfo
	return c.getJSON(ctx, "/api/streams", &out)
}

func (c *client) getJSON(ctx context.Context, path string, v any) error {
	u := c.base.ResolveReference(&url.URL{Path: path})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("go2rtc api: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("go2rtc api %s: HTTP %d", path, res.StatusCode)
	}
	if err := json.NewDecoder(res.Body).Decode(v); err != nil {
		return fmt.Errorf("go2rtc api %s: decode: %w", path, err)
	}
	return nil
}
