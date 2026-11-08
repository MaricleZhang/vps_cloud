package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type PanelClient struct {
	BaseURL, Token string
	HTTP           *http.Client
}

func newPanelClient(base, token string) (*PanelClient, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("invalid panel URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1")) {
		return nil, fmt.Errorf("panel requires HTTPS (HTTP allowed only on loopback)")
	}
	if token == "" {
		return nil, fmt.Errorf("NICECLOUD_NODE_TOKEN or -token-file is required")
	}
	return &PanelClient{BaseURL: strings.TrimRight(base, "/"), Token: token, HTTP: &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *PanelClient) do(ctx context.Context, method, path string, body, output any) error {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+"/api/node/"+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("panel %s returned HTTP %d", path, resp.StatusCode)
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, 8<<20))
	if err = dec.Decode(&envelope); err != nil {
		return err
	}
	if output != nil {
		return json.Unmarshal(envelope.Data, output)
	}
	return nil
}
func (c *PanelClient) policy(ctx context.Context) (Policy, error) {
	var p Policy
	err := c.do(ctx, "GET", "policy", nil, &p)
	return p, err
}
func (c *PanelClient) report(ctx context.Context, samples []Sample) (Policy, error) {
	var out struct {
		Accepted int    `json:"accepted"`
		Policy   Policy `json:"policy"`
	}
	err := c.do(ctx, "POST", "traffic", map[string]any{"samples": samples}, &out)
	if err == nil && out.Accepted != len(samples) {
		err = fmt.Errorf("panel accepted %d of %d samples; retaining outbox", out.Accepted, len(samples))
	}
	return out.Policy, err
}
func (c *PanelClient) ack(ctx context.Context, users []Ack) error {
	return c.do(ctx, "POST", "ack", map[string]any{"users": users}, nil)
}
