package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// PanelClient talks to a 3x-ui panel REST API.
type PanelClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

func NewPanelClient(baseURL, token string) *PanelClient {
	return &PanelClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

type panelResponse struct {
	Success bool            `json:"success"`
	Msg     string          `json:"msg"`
	Obj     json.RawMessage `json:"obj"`
}

type clientPayload struct {
	Client     panelClient `json:"client"`
	InboundIds []int       `json:"inboundIds"`
}

type panelClient struct {
	Email      string `json:"email"`
	Password   string `json:"password"`
	Enable     bool   `json:"enable"`
	TotalGB    int64  `json:"totalGB"`
	ExpiryTime int64  `json:"expiryTime"`
	Comment    string `json:"comment"`
}

type panelTraffic struct {
	Email      string `json:"email"`
	Up         int64  `json:"up"`
	Down       int64  `json:"down"`
	Total      int64  `json:"total"`
	ExpiryTime int64  `json:"expiryTime"`
	Enable     bool   `json:"enable"`
}

type panelInbound struct {
	ID       int             `json:"id"`
	Settings json.RawMessage `json:"settings"`
}

type shadowsocksInboundSettings struct {
	Password string `json:"password"`
}

var (
	errClientNotFound     = errors.New("client not found")
	errClientDisabled     = errors.New("client disabled")
	errNoPassword         = errors.New("client has no shadowsocks password")
	errNoInbound          = errors.New("client has no inbound")
	errNoInboundPassword  = errors.New("inbound has no shadowsocks password")
)

// GetClient fetches a client by email via GET /panel/api/clients/get/{email}.
func (p *PanelClient) GetClient(email string) (*panelClient, []int, error) {
	var payload clientPayload
	if err := p.getJSON("/panel/api/clients/get/"+url.PathEscape(email), &payload); err != nil {
		return nil, nil, err
	}
	if payload.Client.Email == "" {
		return nil, nil, errClientNotFound
	}
	if !payload.Client.Enable {
		return nil, nil, errClientDisabled
	}
	if payload.Client.Password == "" {
		return nil, nil, errNoPassword
	}
	if len(payload.InboundIds) == 0 {
		return nil, nil, errNoInbound
	}
	return &payload.Client, payload.InboundIds, nil
}

// GetTraffic fetches traffic counters via GET /panel/api/clients/traffic/{email}.
func (p *PanelClient) GetTraffic(email string) (*panelTraffic, error) {
	var traffic panelTraffic
	if err := p.getJSON("/panel/api/clients/traffic/"+url.PathEscape(email), &traffic); err != nil {
		return nil, err
	}
	return &traffic, nil
}

// GetInboundPassword returns the Shadowsocks server password from
// GET /panel/api/inbounds/get/{id} (settings.password).
func (p *PanelClient) GetInboundPassword(id int) (string, error) {
	var inbound panelInbound
	if err := p.getJSON(fmt.Sprintf("/panel/api/inbounds/get/%d", id), &inbound); err != nil {
		return "", err
	}

	settingsRaw, err := unwrapJSON(inbound.Settings)
	if err != nil {
		return "", fmt.Errorf("inbound settings: %w", err)
	}
	var settings shadowsocksInboundSettings
	if err := json.Unmarshal(settingsRaw, &settings); err != nil {
		return "", fmt.Errorf("inbound settings: %w", err)
	}
	if settings.Password == "" {
		return "", errNoInboundPassword
	}
	return settings.Password, nil
}

// ss2022Password builds the client password for 2022-blake3-* methods:
// inbound server password + ":" + client password.
func ss2022Password(inboundPassword, clientPassword string) string {
	return inboundPassword + ":" + clientPassword
}

// unwrapJSON accepts either a JSON object/array or a JSON string containing JSON.
func unwrapJSON(raw json.RawMessage) (json.RawMessage, error) {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return nil, fmt.Errorf("empty")
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		return json.RawMessage(s), nil
	}
	return raw, nil
}

func (p *PanelClient) getJSON(path string, dest any) error {
	req, err := http.NewRequest(http.MethodGet, p.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Accept", "application/json")

	res, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}

	if res.StatusCode == http.StatusNotFound {
		return errClientNotFound
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("panel returned HTTP %d: %s", res.StatusCode, truncate(string(body), 200))
	}

	var wrap panelResponse
	if err := json.Unmarshal(body, &wrap); err != nil {
		return fmt.Errorf("decode panel response: %w", err)
	}
	if !wrap.Success {
		msg := strings.TrimSpace(wrap.Msg)
		if msg == "" {
			msg = "request failed"
		}
		lower := strings.ToLower(msg)
		if strings.Contains(lower, "not found") || strings.Contains(lower, "record not found") {
			return errClientNotFound
		}
		return fmt.Errorf("panel: %s", msg)
	}
	if len(wrap.Obj) == 0 || string(wrap.Obj) == "null" {
		return errClientNotFound
	}
	if err := json.Unmarshal(wrap.Obj, dest); err != nil {
		return fmt.Errorf("decode panel obj: %w", err)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
