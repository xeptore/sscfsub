package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/xtls/xray-core/infra/conf"
	"gopkg.in/yaml.v3"
)

// SubscriptionEntry is an Xray config plus client-only metadata.
// remarks is used by subscription UIs and is not part of conf.Config.
type SubscriptionEntry struct {
	conf.Config
	Remarks string `json:"remarks,omitempty"`
}

type hostEntry struct {
	Name string
	IPs  []string
}

type clientCreds struct {
	Email    string
	Password string
}

type clientUsage struct {
	Up         int64
	Down       int64
	Total      int64 // 0 = unlimited
	ExpiryTime int64 // unix ms; 0 = no expiry
}

func loadTemplate(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Validate against Xray's official config types.
	var check SubscriptionEntry
	if err := json.Unmarshal(data, &check); err != nil {
		return nil, fmt.Errorf("invalid Xray config: %w", err)
	}

	// Keep the template as a generic JSON object so remarsaling does not
	// inject zero/null fields from conf struct defaults.
	var entry map[string]any
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, err
	}
	return entry, nil
}

// loadHosts preserves the order of keys as written in the YAML file.
func loadHosts(path string) ([]hostEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil, fmt.Errorf("%s: empty document", path)
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: expected a mapping of hostname → IP list", path)
	}

	hosts := make([]hostEntry, 0, len(doc.Content)/2)
	for i := 0; i < len(doc.Content); i += 2 {
		key := doc.Content[i]
		val := doc.Content[i+1]

		var ips []string
		if err := val.Decode(&ips); err != nil {
			return nil, fmt.Errorf("%s: host %q: %w", path, key.Value, err)
		}
		hosts = append(hosts, hostEntry{Name: key.Value, IPs: ips})
	}
	return hosts, nil
}

func buildSubscription(templatePath, hostsPath string, creds clientCreds, usage clientUsage) ([]map[string]any, error) {
	prototype, err := loadTemplate(templatePath)
	if err != nil {
		return nil, fmt.Errorf("load template: %w", err)
	}
	hosts, err := loadHosts(hostsPath)
	if err != nil {
		return nil, fmt.Errorf("load hosts: %w", err)
	}

	location, err := time.LoadLocation("Asia/Tehran")
	if err != nil {
		return nil, fmt.Errorf("load location: %w", err)
	}

	configs := make([]map[string]any, 0, len(hosts)+3)
	configs = append(configs,
		infoEntry(usageRemark(usage)),
		infoEntry("🔄 Last Updated: "+time.Now().In(location).Format("2006/01/02 15:04")),
	)

	for _, h := range hosts {
		cfg, err := cloneJSON(prototype)
		if err != nil {
			return nil, fmt.Errorf("clone template for %q: %w", h.Name, err)
		}
		if err := applyHost(cfg, h.Name, h.IPs, creds); err != nil {
			return nil, fmt.Errorf("apply host %q: %w", h.Name, err)
		}
		cfg["remarks"] = "🔗 " + h.Name
		configs = append(configs, cfg)
	}
	return configs, nil
}

// infoEntry is a placeholder config whose remarks are shown in subscription UIs.
func infoEntry(remarks string) map[string]any {
	return map[string]any{
		"remarks": remarks,
		"outbounds": []any{
			map[string]any{
				"protocol": "block",
				"tag":      "block",
			},
		},
	}
}

func usageRemark(u clientUsage) string {
	used := u.Up + u.Down

	var traffic string
	switch {
	case u.Total <= 0:
		traffic = fmt.Sprintf("📊 %s / ∞", formatBytes(used))
	default:
		traffic = fmt.Sprintf("📊 %s / %s", formatBytes(used), formatBytes(u.Total))
	}

	var expiry string
	switch {
	case u.ExpiryTime <= 0:
		expiry = "📅 ∞"
	default:
		expiry = "📅 " + time.UnixMilli(u.ExpiryTime).UTC().Format("2006/01/02")
	}

	return traffic + " · " + expiry
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func cloneJSON(src map[string]any) (map[string]any, error) {
	b, err := json.Marshal(src)
	if err != nil {
		return nil, err
	}
	var dst map[string]any
	if err := json.Unmarshal(b, &dst); err != nil {
		return nil, err
	}
	return dst, nil
}

func applyHost(cfg map[string]any, host string, ips []string, creds clientCreds) error {
	if err := setOutboundServer(cfg, host, creds); err != nil {
		return err
	}
	return setDNSHosts(cfg, host, ips)
}

func setOutboundServer(cfg map[string]any, host string, creds clientCreds) error {
	outbounds, err := asSlice(cfg["outbounds"], "outbounds")
	if err != nil {
		return err
	}
	if len(outbounds) == 0 {
		return fmt.Errorf("outbounds: empty")
	}

	outbound, err := asMap(outbounds[0], "outbounds[0]")
	if err != nil {
		return err
	}
	settingsVal, ok := outbound["settings"]
	if !ok {
		return fmt.Errorf("outbounds[0].settings: missing")
	}

	// Validate with the official Shadowsocks client settings type, then
	// patch only the fields we own so defaults are not written back.
	settingsJSON, err := json.Marshal(settingsVal)
	if err != nil {
		return err
	}
	var settings conf.ShadowsocksClientConfig
	if err := json.Unmarshal(settingsJSON, &settings); err != nil {
		return fmt.Errorf("outbounds[0].settings: %w", err)
	}

	settingsMap, err := asMap(settingsVal, "outbounds[0].settings")
	if err != nil {
		return err
	}
	switch {
	case len(settings.Servers) > 0:
		servers, err := asSlice(settingsMap["servers"], "outbounds[0].settings.servers")
		if err != nil {
			return err
		}
		server, err := asMap(servers[0], "outbounds[0].settings.servers[0]")
		if err != nil {
			return err
		}
		server["address"] = host
		server["password"] = creds.Password
		server["email"] = creds.Email
	default:
		settingsMap["address"] = host
		settingsMap["password"] = creds.Password
		settingsMap["email"] = creds.Email
	}
	return nil
}

func setDNSHosts(cfg map[string]any, host string, ips []string) error {
	dnsVal, ok := cfg["dns"]
	if !ok {
		return fmt.Errorf("dns: missing")
	}
	dns, err := asMap(dnsVal, "dns")
	if err != nil {
		return err
	}

	// Validate through HostsWrapper, then store the plain mapping.
	raw, err := json.Marshal(map[string][]string{host: ips})
	if err != nil {
		return err
	}
	var hosts conf.HostsWrapper
	if err := json.Unmarshal(raw, &hosts); err != nil {
		return fmt.Errorf("dns.hosts: %w", err)
	}

	ipList := make([]any, len(ips))
	for i, ip := range ips {
		ipList[i] = ip
	}
	dns["hosts"] = map[string]any{host: ipList}
	return nil
}

func asMap(v any, path string) (map[string]any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: expected object", path)
	}
	return m, nil
}

func asSlice(v any, path string) ([]any, error) {
	s, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%s: expected array", path)
	}
	return s, nil
}
