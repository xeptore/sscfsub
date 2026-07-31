package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGetClient(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /panel/api/clients/get/{email}", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"obj": map[string]any{
				"client": map[string]any{
					"email":    "alice@example.com",
					"password": "ss-secret",
					"enable":   true,
				},
			},
		})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	client, err := NewPanelClient(ts.URL, "test-token").GetClient("alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if client.Email != "alice@example.com" || client.Password != "ss-secret" {
		t.Fatalf("got %+v", client)
	}
}

func TestGetTraffic(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /panel/api/clients/traffic/{email}", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"obj": map[string]any{
				"email":      "alice@example.com",
				"up":         1024,
				"down":       2048,
				"total":      10737418240,
				"expiryTime": time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC).UnixMilli(),
				"enable":     true,
			},
		})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	tr, err := NewPanelClient(ts.URL, "tok").GetTraffic("alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Up != 1024 || tr.Down != 2048 || tr.Total != 10737418240 {
		t.Fatalf("got %+v", tr)
	}
}

func TestBuildSubscriptionSetsCredsAndInfo(t *testing.T) {
	expiry := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC).UnixMilli()
	configs, err := buildSubscription("subscription.tpl.json", "hosts.yaml", clientCreds{
		Email:    "alice@example.com",
		Password: "ss-secret",
	}, clientUsage{
		Up:         1024 * 1024 * 1024,
		Down:       1024 * 1024 * 1024,
		Total:      10 * 1024 * 1024 * 1024,
		ExpiryTime: expiry,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) < 3 {
		t.Fatalf("len = %d", len(configs))
	}

	usageRemarks, _ := configs[0]["remarks"].(string)
	if !strings.Contains(usageRemarks, "📊") ||
		!strings.Contains(usageRemarks, "📅 2026/12/31") {
		t.Fatalf("usage remarks = %q", usageRemarks)
	}
	updatedRemarks, _ := configs[1]["remarks"].(string)
	if !strings.HasPrefix(updatedRemarks, "🔄 ") {
		t.Fatalf("updated remarks = %q", updatedRemarks)
	}

	var hostCfg map[string]any
	for _, cfg := range configs {
		remarks, _ := cfg["remarks"].(string)
		if strings.HasPrefix(remarks, "🔗 ") {
			hostCfg = cfg
			break
		}
	}
	if hostCfg == nil {
		t.Fatal("no host config found")
	}
	server := hostCfg["outbounds"].([]any)[0].(map[string]any)["settings"].(map[string]any)["servers"].([]any)[0].(map[string]any)
	if server["password"] != "ss-secret" {
		t.Fatalf("password = %v", server["password"])
	}
	if server["email"] != "alice@example.com" {
		t.Fatalf("email = %v", server["email"])
	}
	if !strings.HasPrefix(hostCfg["remarks"].(string), "🔗 ") {
		t.Fatalf("remarks = %v", hostCfg["remarks"])
	}
}

func TestUsageRemarkUnlimited(t *testing.T) {
	got := usageRemark(clientUsage{Up: 100, Down: 200})
	if !strings.Contains(got, "∞") || !strings.Contains(got, "📅 ∞") {
		t.Fatalf("got %q", got)
	}
}

func TestSubscriptionHeaders(t *testing.T) {
	cfg, err := loadConfig("config.yaml")
	if err != nil {
		t.Fatal(err)
	}

	expiryMS := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC).UnixMilli()
	usage := clientUsage{Up: 0, Down: 14332111, Total: 107374182400, ExpiryTime: expiryMS}

	r := httptest.NewRequest(http.MethodGet, "https://sub.example.com/sub?email=alice@example.com", nil)
	r.Host = "sub.example.com"
	w := httptest.NewRecorder()
	setSubscriptionHeaders(w, r, cfg, "alice@example.com", usage, " VIP Plan | note")

	h := w.Header()
	wantTitle := cfg.ProfileTitle + " - VIP Plan"
	if got := h.Get("profile-title"); got != base64Header(wantTitle) {
		t.Fatalf("profile-title = %q want %q", got, base64Header(wantTitle))
	}
	if got := h.Get("announce"); !strings.HasPrefix(got, "base64:") {
		t.Fatalf("announce = %q", got)
	}
	if got := h.Get("Content-Disposition"); got != `attachment; filename="alice@example.com.json"` {
		t.Fatalf("content-disposition = %q", got)
	}
	if got := h.Get("profile-update-interval"); got != strconv.Itoa(cfg.ProfileUpdateInterval) {
		t.Fatalf("profile-update-interval = %q", got)
	}
	if got := h.Get("profile-web-page-url"); got != "https://sub.example.com/sub?email=alice@example.com" {
		t.Fatalf("profile-web-page-url = %q", got)
	}
	if got := h.Get("server"); got != "cloudflare" {
		t.Fatalf("server = %q", got)
	}
	wantInfo := fmt.Sprintf("upload=0; download=14332111; total=107374182400; expire=%d", expiryMS/1000)
	if got := h.Get("subscription-userinfo"); got != wantInfo {
		t.Fatalf("subscription-userinfo = %q want %q", got, wantInfo)
	}
	if got := h.Get("support-url"); got != cfg.SupportURL {
		t.Fatalf("support-url = %q", got)
	}
}

func TestProfileTitleFromComment(t *testing.T) {
	tests := []struct {
		base, comment, want string
	}{
		{"Sub", "", "Sub"},
		{"Sub", "   ", "Sub"},
		{"Sub", "VIP", "Sub - VIP"},
		{"Sub", " VIP | extra ", "Sub - VIP"},
		{"Sub", "| only suffix", "Sub"},
	}
	for _, tt := range tests {
		if got := profileTitle(tt.base, tt.comment); got != tt.want {
			t.Fatalf("profileTitle(%q, %q) = %q want %q", tt.base, tt.comment, got, tt.want)
		}
	}
}
