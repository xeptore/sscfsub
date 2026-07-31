package main

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type appConfig struct {
	Announce              string `yaml:"announce"`
	AnnounceURL           string `yaml:"announce_url"`
	ProfileTitle          string `yaml:"profile_title"`
	ProfileUpdateInterval int    `yaml:"profile_update_interval"` // hours
	SupportURL            string `yaml:"support_url"`
	Server                string `yaml:"server"`
}

func loadConfig(path string) (*appConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg appConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.ProfileUpdateInterval <= 0 {
		cfg.ProfileUpdateInterval = 12
	}
	if cfg.Server == "" {
		cfg.Server = "cloudflare"
	}
	if cfg.ProfileTitle == "" {
		cfg.ProfileTitle = "Subscription"
	}
	return &cfg, nil
}

func base64Header(plain string) string {
	return "base64:" + base64.StdEncoding.EncodeToString([]byte(plain))
}

func subscriptionUserinfo(u clientUsage) string {
	expire := int64(0)
	if u.ExpiryTime > 0 {
		expire = u.ExpiryTime
		// 3x-ui stores expiry in unix milliseconds.
		if expire > 1_000_000_000_000 {
			expire /= 1000
		}
	}
	return fmt.Sprintf("upload=%d; download=%d; total=%d; expire=%d",
		u.Up, u.Down, u.Total, expire)
}

func contentDispositionFilename(email string) string {
	name := strings.ReplaceAll(email, `"`, "")
	return fmt.Sprintf(`attachment; filename="%s.json"`, name)
}

func requestURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = strings.TrimSpace(strings.Split(proto, ",")[0])
	}
	host := r.Host
	if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
		host = strings.TrimSpace(strings.Split(fwd, ",")[0])
	}
	return scheme + "://" + host + r.URL.RequestURI()
}

func setSubscriptionHeaders(w http.ResponseWriter, r *http.Request, cfg *appConfig, email string, usage clientUsage, comment string) {
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Content-Disposition", contentDispositionFilename(email))
	h.Set("profile-update-interval", strconv.Itoa(cfg.ProfileUpdateInterval))
	h.Set("profile-web-page-url", requestURL(r))
	h.Set("profile-title", base64Header(profileTitle(cfg.ProfileTitle, comment)))
	h.Set("subscription-userinfo", subscriptionUserinfo(usage))
	h.Set("server", cfg.Server)

	if cfg.Announce != "" {
		h.Set("announce", base64Header(strings.TrimSpace(cfg.Announce)))
	}
	if cfg.AnnounceURL != "" {
		h.Set("announce-url", cfg.AnnounceURL)
	}
	if cfg.SupportURL != "" {
		h.Set("support-url", cfg.SupportURL)
	}
}

// profileTitle appends the first `|`-separated segment of comment (trimmed), if any.
func profileTitle(base, comment string) string {
	suffix := commentTitleSuffix(comment)
	if suffix == "" {
		return base
	}
	return base + " - " + suffix
}

func commentTitleSuffix(comment string) string {
	part, _, _ := strings.Cut(comment, "|")
	return strings.TrimSpace(part)
}
