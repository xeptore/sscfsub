package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
)

func main() {
	listen := flag.String("listen", envOr("LISTEN", ":8080"), "HTTP listen address")
	panelURL := flag.String("panel-url", envOr("PANEL_URL", ""), "3x-ui panel base URL (including webBasePath if any)")
	panelToken := flag.String("panel-token", envOr("PANEL_TOKEN", ""), "3x-ui API bearer token")
	templatePath := flag.String("template", envOr("TEMPLATE", "subscription.tpl.json"), "path to subscription JSON template")
	hostsPath := flag.String("hosts", envOr("HOSTS", "hosts.yaml"), "path to hosts YAML (hostname → IP list)")
	configPath := flag.String("config", envOr("CONFIG", "config.yaml"), "path to app config (subscription headers)")
	flag.Parse()

	if strings.TrimSpace(*panelURL) == "" {
		fmt.Fprintln(os.Stderr, "error: -panel-url or PANEL_URL is required")
		os.Exit(2)
	}
	if strings.TrimSpace(*panelToken) == "" {
		fmt.Fprintln(os.Stderr, "error: -panel-token or PANEL_TOKEN is required")
		os.Exit(2)
	}

	// Fail fast if local files are missing / invalid.
	if _, err := loadConfig(*configPath); err != nil {
		fmt.Fprintf(os.Stderr, "error: load config: %v\n", err)
		os.Exit(1)
	}
	if _, err := loadTemplate(*templatePath); err != nil {
		fmt.Fprintf(os.Stderr, "error: load template: %v\n", err)
		os.Exit(1)
	}
	if _, err := loadHosts(*hostsPath); err != nil {
		fmt.Fprintf(os.Stderr, "error: load hosts: %v\n", err)
		os.Exit(1)
	}

	srv := &server{
		panel:        NewPanelClient(*panelURL, *panelToken),
		templatePath: *templatePath,
		hostsPath:    *hostsPath,
		configPath:   *configPath,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", srv.handleHealthz)
	mux.HandleFunc("GET /sub", srv.handleSub)
	mux.HandleFunc("GET /sub/{email}", srv.handleSub)

	log.Printf("listening on %s", *listen)
	if err := http.ListenAndServe(*listen, mux); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

type server struct {
	panel        *PanelClient
	templatePath string
	hostsPath    string
	configPath   string
}

func (s *server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (s *server) handleSub(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(r.PathValue("email"))
	if email == "" {
		email = strings.TrimSpace(r.URL.Query().Get("email"))
	}
	if email == "" {
		http.Error(w, "missing email", http.StatusBadRequest)
		return
	}
	if decoded, err := url.PathUnescape(email); err == nil {
		email = decoded
	}

	client, err := s.panel.GetClient(email)
	if err != nil {
		switch {
		case errors.Is(err, errClientNotFound):
			http.Error(w, "client not found", http.StatusNotFound)
		case errors.Is(err, errClientDisabled):
			http.Error(w, "client disabled", http.StatusForbidden)
		case errors.Is(err, errNoPassword):
			http.Error(w, "client has no password", http.StatusUnprocessableEntity)
		default:
			log.Printf("panel lookup %q: %v", email, err)
			http.Error(w, "panel lookup failed", http.StatusBadGateway)
		}
		return
	}

	traffic, err := s.panel.GetTraffic(email)
	if err != nil {
		log.Printf("panel traffic %q: %v", email, err)
		http.Error(w, "panel traffic lookup failed", http.StatusBadGateway)
		return
	}

	usage := clientUsage{
		Up:         traffic.Up,
		Down:       traffic.Down,
		Total:      traffic.Total,
		ExpiryTime: traffic.ExpiryTime,
	}
	// Prefer get/{email} quota/expiry when traffic row has none.
	if usage.Total == 0 && client.TotalGB > 0 {
		usage.Total = client.TotalGB
	}
	if usage.ExpiryTime == 0 && client.ExpiryTime > 0 {
		usage.ExpiryTime = client.ExpiryTime
	}

	configs, err := buildSubscription(s.templatePath, s.hostsPath, clientCreds{
		Email:    client.Email,
		Password: client.Password,
	}, usage)
	if err != nil {
		log.Printf("build subscription for %q: %v", email, err)
		http.Error(w, "failed to build subscription", http.StatusInternalServerError)
		return
	}

	cfg, err := loadConfig(s.configPath)
	if err != nil {
		log.Printf("load config: %v", err)
		http.Error(w, "failed to load config", http.StatusInternalServerError)
		return
	}
	setSubscriptionHeaders(w, r, cfg, client.Email, usage, client.Comment)

	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "")
	if err := enc.Encode(configs); err != nil {
		log.Printf("encode subscription for %q: %v", email, err)
	}
}
