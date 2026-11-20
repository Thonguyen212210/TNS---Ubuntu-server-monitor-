package main

// Demo mode: synthetic, clearly-labelled data for subsystems that are not present on
// the current machine (e.g. previewing the panel on macOS). Real data always wins
// when the subsystem exists. IPs use RFC 5737 documentation ranges.

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var demoMu sync.Mutex

type demoLoc struct {
	ip              string
	g               Geo
	attacker        bool
	fails, okPerDay int
	user            string
}

var demoLocs = []demoLoc{
	{"198.51.100.10", Geo{"Vietnam", "VN", "Ho Chi Minh City", 10.82, 106.63, false}, false, 2, 3, "ops"},
	{"198.51.100.11", Geo{"Vietnam", "VN", "Hanoi", 21.03, 105.85, false}, false, 0, 1, "ops"},
	{"198.51.100.20", Geo{"Singapore", "SG", "Singapore", 1.35, 103.82, false}, false, 0, 4, "deploy"},
	{"203.0.113.45", Geo{"Russia", "RU", "Moscow", 55.75, 37.62, false}, true, 64, 0, ""},
	{"203.0.113.12", Geo{"China", "CN", "Beijing", 39.90, 116.40, false}, true, 412, 0, ""},
	{"203.0.113.13", Geo{"China", "CN", "Shanghai", 31.23, 121.47, false}, true, 230, 0, ""},
	{"203.0.113.14", Geo{"China", "CN", "Shenzhen", 22.54, 114.06, false}, true, 96, 0, ""},
	{"203.0.113.21", Geo{"United States", "US", "Ashburn", 39.04, -77.49, false}, true, 180, 0, ""},
	{"203.0.113.22", Geo{"United States", "US", "Los Angeles", 34.05, -118.24, false}, true, 52, 0, ""},
	{"203.0.113.23", Geo{"United States", "US", "Dallas", 32.78, -96.80, false}, true, 33, 0, ""},
	{"203.0.113.31", Geo{"Netherlands", "NL", "Amsterdam", 52.37, 4.90, false}, true, 140, 0, ""},
	{"203.0.113.32", Geo{"Germany", "DE", "Frankfurt", 50.11, 8.68, false}, true, 75, 0, ""},
	{"203.0.113.33", Geo{"France", "FR", "Paris", 48.86, 2.35, false}, true, 21, 0, ""},
	{"203.0.113.34", Geo{"United Kingdom", "GB", "London", 51.51, -0.13, false}, true, 18, 0, ""},
	{"203.0.113.41", Geo{"Brazil", "BR", "São Paulo", -23.55, -46.63, false}, true, 120, 0, ""},
	{"203.0.113.42", Geo{"India", "IN", "Mumbai", 19.08, 72.88, false}, true, 160, 0, ""},
	{"203.0.113.43", Geo{"South Korea", "KR", "Seoul", 37.57, 126.98, false}, true, 88, 0, ""},
	{"203.0.113.44", Geo{"Indonesia", "ID", "Jakarta", -6.21, 106.85, false}, true, 66, 0, ""},
	{"203.0.113.51", Geo{"Iran", "IR", "Tehran", 35.69, 51.39, false}, true, 30, 0, ""},
	{"203.0.113.52", Geo{"Nigeria", "NG", "Lagos", 6.52, 3.38, false}, true, 14, 0, ""},
	{"203.0.113.53", Geo{"Turkey", "TR", "Istanbul", 41.01, 28.98, false}, true, 41, 0, ""},
	{"203.0.113.54", Geo{"Japan", "JP", "Tokyo", 35.68, 139.69, false}, true, 12, 0, ""},
	{"203.0.113.55", Geo{"Australia", "AU", "Sydney", -33.87, 151.21, false}, true, 9, 0, ""},
	{"203.0.113.56", Geo{"South Africa", "ZA", "Johannesburg", -26.20, 28.05, false}, true, 11, 0, ""},
	{"203.0.113.57", Geo{"Mexico", "MX", "Mexico City", 19.43, -99.13, false}, true, 17, 0, ""},
	{"203.0.113.58", Geo{"Romania", "RO", "Bucharest", 44.43, 26.10, false}, true, 47, 0, ""},
	{"203.0.113.59", Geo{"Thailand", "TH", "Bangkok", 13.76, 100.50, false}, true, 23, 0, ""},
	{"203.0.113.60", Geo{"Canada", "CA", "Toronto", 43.65, -79.38, false}, true, 8, 0, ""},
	{"192.0.2.77", Geo{"Netherlands", "NL", "Rotterdam", 51.92, 4.48, false}, false, 0, 0, "ops"},
}

func demoGeo(ip string) (Geo, bool) {
	if !*flagDemo {
		return Geo{}, false
	}
	for _, l := range demoLocs {
		if l.ip == ip {
			return l.g, true
		}
	}
	return Geo{}, false
}

func demoLoginEvents() []LoginEvent {
	rng := rand.New(rand.NewSource(1611))
	now := time.Now()
	users := []string{"root", "root", "root", "admin", "ubuntu", "test", "oracle", "postgres", "user", "git", "pi", "ftpuser", "debian", "support"}
	var evs []LoginEvent
	for _, l := range demoLocs {
		if l.attacker {
			for i := 0; i < l.fails; i++ {
				// attacks cluster in bursts, biased to recent days
				age := time.Duration(rng.ExpFloat64()*6*24) * time.Hour
				if age > 29*24*time.Hour {
					age = time.Duration(rng.Intn(29*24)) * time.Hour
				}
				t := now.Add(-age - time.Duration(rng.Intn(3600))*time.Second)
				m := "password"
				if rng.Intn(3) == 0 {
					m = "invalid-user"
				}
				evs = append(evs, LoginEvent{T: t, IP: l.ip, User: users[rng.Intn(len(users))], Method: m, Service: "ssh"})
			}
			continue
		}
		for d := 0; d < 30; d++ {
			for k := 0; k < l.okPerDay; k++ {
				if rng.Intn(4) == 0 {
					continue
				}
				h := 8 + rng.Intn(12)
				t := time.Date(now.Year(), now.Month(), now.Day(), h, rng.Intn(60), rng.Intn(60), 0, time.Local).AddDate(0, 0, -d)
				if t.After(now) {
					continue
				}
				evs = append(evs, LoginEvent{T: t, IP: l.ip, User: l.user, OK: true, Method: "publickey", Service: "ssh"})
			}
		}
		for i := 0; i < l.fails; i++ {
			evs = append(evs, LoginEvent{T: now.Add(-time.Duration(rng.Intn(20*24)) * time.Hour), IP: l.ip, User: l.user, Method: "password", Service: "ssh"})
		}
	}
	// anomaly 1: brute force from Moscow that finally succeeded
	evs = append(evs, LoginEvent{T: now.Add(-5*time.Hour - 12*time.Minute), IP: "203.0.113.45", User: "deploy", OK: true, Method: "password", Service: "ssh"})
	// anomaly 2: first-ever login from a new country
	evs = append(evs, LoginEvent{T: now.Add(-26 * time.Hour), IP: "192.0.2.77", User: "ops", OK: true, Method: "publickey", Service: "ssh"})
	sort.Slice(evs, func(i, j int) bool { return evs[i].T.Before(evs[j].T) })
	return evs
}

// ---------- firewall ----------

var demoFW = &FWState{Backend: "demo", Active: true, Default: "deny (incoming), allow (outgoing), disabled (routed)",
	Note: "Demo mode — manages real ufw / firewalld / iptables on Linux.",
	Rules: []FWRule{
		{To: "22/tcp", Action: "ALLOW IN", From: "Anywhere", Comment: "ssh"},
		{To: "80/tcp", Action: "ALLOW IN", From: "Anywhere", Comment: "http"},
		{To: "443/tcp", Action: "ALLOW IN", From: "Anywhere", Comment: "https"},
		{To: "1611/tcp", Action: "ALLOW IN", From: "Anywhere", Comment: "tns panel"},
		{To: "5432/tcp", Action: "ALLOW IN", From: "10.0.0.0/8", Comment: "postgres LAN"},
		{To: "Anywhere", Action: "DENY IN", From: "203.0.113.12", Comment: "brute force"},
		{To: "22/tcp (v6)", Action: "ALLOW IN", From: "Anywhere (v6)", V6: true},
		{To: "80/tcp (v6)", Action: "ALLOW IN", From: "Anywhere (v6)", V6: true},
	}}

func renumber() {
	for i := range demoFW.Rules {
		demoFW.Rules[i].Num = i + 1
		demoFW.Rules[i].ID = fmt.Sprint(i + 1)
	}
}

func demoFirewall() FWState {
	demoMu.Lock()
	defer demoMu.Unlock()
	renumber()
	c := *demoFW
	c.Rules = append([]FWRule(nil), demoFW.Rules...)
	return c
}

func demoFirewallAction(q fwReq) (string, error) {
	demoMu.Lock()
	defer demoMu.Unlock()
	renumber()
	switch q.Op {
	case "enable":
		demoFW.Active = true
		return "Firewall is active and enabled on system startup", nil
	case "disable":
		demoFW.Active = false
		return "Firewall stopped and disabled on system startup", nil
	case "default":
		demoFW.Default = q.Policy + " (incoming), allow (outgoing), disabled (routed)"
		return "Default incoming policy changed to '" + q.Policy + "'", nil
	case "delete":
		for i, r := range demoFW.Rules {
			if r.ID == q.ID {
				demoFW.Rules = append(demoFW.Rules[:i], demoFW.Rules[i+1:]...)
				return "Rule deleted", nil
			}
		}
		return "", fmt.Errorf("rule not found")
	case "add":
		to := "Anywhere"
		if q.Port != "" {
			to = q.Port
			if q.Proto != "any" {
				to += "/" + q.Proto
			}
		}
		from := q.From
		if from == "" || from == "any" {
			from = "Anywhere"
		}
		r := FWRule{To: to, Action: strings.ToUpper(q.Action) + " IN", From: from, Comment: q.Comment}
		if q.Action == "deny" || q.Action == "reject" {
			demoFW.Rules = append([]FWRule{r}, demoFW.Rules...)
		} else {
			demoFW.Rules = append(demoFW.Rules, r)
		}
		return "Rule added", nil
	}
	return "", fmt.Errorf("invalid operation")
}

// ---------- docker ----------

var demoContainers = []Container{
	{ID: "a1f3c9e2b7d4", Name: "nginx-proxy", Image: "nginx:1.27-alpine", State: "running", Ports: []string{"80→80/tcp", "443→443/tcp"}, Compose: "edge", MemLim: 512 << 20},
	{ID: "b28e41d09c33", Name: "shop-web", Image: "node:20-alpine", State: "running", Ports: []string{"3000/tcp"}, Compose: "shop", MemLim: 1 << 30},
	{ID: "c90d77a1e5f2", Name: "shop-db", Image: "postgres:16", State: "running", Ports: []string{"5432/tcp"}, Compose: "shop", MemLim: 2 << 30},
	{ID: "d4471be8a0c6", Name: "shop-cache", Image: "redis:7-alpine", State: "running", Ports: []string{"6379/tcp"}, Compose: "shop", MemLim: 256 << 20},
	{ID: "e5c2a9f81d07", Name: "uptime-kuma", Image: "louislam/uptime-kuma:1", State: "running", Ports: []string{"3001→3001/tcp"}, MemLim: 512 << 20},
	{ID: "f61b0d3c4e98", Name: "grafana", Image: "grafana/grafana:11.3.0", State: "exited", Ports: []string{}, MemLim: 512 << 20},
	{ID: "0a7e5f2d9b11", Name: "backup-job", Image: "alpine:3.20", State: "exited", Ports: []string{}},
}

var demoCreated = time.Now().Add(-9 * 24 * time.Hour)

func demoDocker() map[string]any {
	demoMu.Lock()
	defer demoMu.Unlock()
	out := make([]Container, len(demoContainers))
	for i, c := range demoContainers {
		c.Created = demoCreated.Add(time.Duration(i) * time.Hour).Unix()
		if c.State == "running" {
			c.CPU = round1(rand.Float64()*[]float64{3, 18, 6, 1.5, 2, 0, 0}[i] + 0.1)
			c.Mem = uint64([]float64{24, 180, 310, 12, 96, 0, 0}[i]*(0.95+rand.Float64()*0.1)) << 20
			c.Status = "Up 9 days"
		} else if c.State == "paused" {
			c.Status = "Up 9 days (Paused)"
		} else {
			c.Status = "Exited (0) 2 days ago"
		}
		out[i] = c
	}
	images := []Image{
		{ID: "3b25b682ea82", Tags: []string{"nginx:1.27-alpine"}, Size: 48 << 20},
		{ID: "5f2c9a1d77e0", Tags: []string{"node:20-alpine"}, Size: 135 << 20},
		{ID: "8e4c0b7f2a91", Tags: []string{"postgres:16"}, Size: 432 << 20},
		{ID: "a7d31c9e0f44", Tags: []string{"redis:7-alpine"}, Size: 41 << 20},
		{ID: "c1e0f8a2b6d3", Tags: []string{"louislam/uptime-kuma:1"}, Size: 450 << 20},
		{ID: "d9b7a4e1c0f2", Tags: []string{"grafana/grafana:11.3.0"}, Size: 470 << 20},
		{ID: "91ef0a7c3b55", Tags: []string{"alpine:3.20"}, Size: 8 << 20},
	}
	for i := range images {
		images[i].Created = demoCreated.Unix()
	}
	return map[string]any{"available": true, "demo": true, "version": "27.3.1 (demo)", "containers": out, "images": images}
}

func demoDockerAction(id, op string) error {
	demoMu.Lock()
	defer demoMu.Unlock()
	for i := range demoContainers {
		c := &demoContainers[i]
		if c.ID != id && c.Name != id {
			continue
		}
		switch op {
		case "start", "restart", "unpause":
			c.State = "running"
		case "stop":
			c.State = "exited"
		case "pause":
			c.State = "paused"
		case "remove":
			demoContainers = append(demoContainers[:i], demoContainers[i+1:]...)
		default:
			return fmt.Errorf("invalid operation")
		}
		return nil
	}
	return fmt.Errorf("container not found")
}

func demoDockerLogs(id string) string {
	var b strings.Builder
	t := time.Now().Add(-10 * time.Minute)
	lines := []string{"GET / 200 12ms", "GET /api/products 200 34ms", "POST /api/cart 201 48ms", "GET /static/app.js 304 2ms", "GET /healthz 200 1ms", "WARN slow query 812ms: SELECT * FROM orders"}
	for i := 0; i < 60; i++ {
		t = t.Add(time.Duration(5+rand.Intn(10)) * time.Second)
		fmt.Fprintf(&b, "%s [%s] %s\n", t.UTC().Format(time.RFC3339Nano), id, lines[rand.Intn(len(lines))])
	}
	return b.String()
}

// ---------- services ----------

var demoSvcs = []Service{
	{"nginx.service", "loaded", "active", "running", "A high performance web server and a reverse proxy server", "enabled"},
	{"docker.service", "loaded", "active", "running", "Docker Application Container Engine", "enabled"},
	{"containerd.service", "loaded", "active", "running", "containerd container runtime", "enabled"},
	{"ssh.service", "loaded", "active", "running", "OpenBSD Secure Shell server", "enabled"},
	{"ufw.service", "loaded", "active", "exited", "Uncomplicated firewall", "enabled"},
	{"cron.service", "loaded", "active", "running", "Regular background program processing daemon", "enabled"},
	{"fail2ban.service", "loaded", "failed", "failed", "Fail2Ban Service", "enabled"},
	{"postgresql.service", "loaded", "inactive", "dead", "PostgreSQL RDBMS", "disabled"},
	{"systemd-journald.service", "loaded", "active", "running", "Journal Service", "static"},
	{"systemd-resolved.service", "loaded", "active", "running", "Network Name Resolution", "enabled"},
	{"rsyslog.service", "loaded", "active", "running", "System Logging Service", "enabled"},
	{"unattended-upgrades.service", "loaded", "active", "running", "Unattended Upgrades Shutdown", "enabled"},
	{"tns.service", "loaded", "active", "running", "TNS server panel", "enabled"},
	{"snapd.service", "loaded", "inactive", "dead", "Snap Daemon", "disabled"},
}

func demoServices() []Service {
	demoMu.Lock()
	defer demoMu.Unlock()
	out := append([]Service(nil), demoSvcs...)
	sort.Slice(out, func(i, j int) bool {
		if activeRank(out[i].Active) != activeRank(out[j].Active) {
			return activeRank(out[i].Active) < activeRank(out[j].Active)
		}
		return out[i].Unit < out[j].Unit
	})
	return out
}

func demoServiceAction(unit, op string) {
	demoMu.Lock()
	defer demoMu.Unlock()
	for i := range demoSvcs {
		s := &demoSvcs[i]
		if s.Unit != unit {
			continue
		}
		switch op {
		case "start", "restart", "reload":
			s.Active, s.Sub = "active", "running"
		case "stop":
			s.Active, s.Sub = "inactive", "dead"
		case "enable":
			s.Enabled = "enabled"
		case "disable":
			s.Enabled = "disabled"
		}
	}
}

func demoServiceLogs(unit string) string {
	t := time.Now().Add(-time.Hour)
	var b strings.Builder
	for i := 0; i < 25; i++ {
		t = t.Add(time.Duration(60+rand.Intn(200)) * time.Second)
		fmt.Fprintf(&b, "%s demo-server %s[%d]: %s\n", t.Format("2006-01-02T15:04:05-0700"), strings.TrimSuffix(unit, ".service"), 1200+i,
			[]string{"Started.", "Reloading configuration", "Worker ready", "Health check OK", "Connection accepted"}[rand.Intn(5)])
	}
	return b.String()
}

// ---------- nginx ----------

func initDemo() {
	if nginxRoot() == "/etc/nginx" {
		return
	}
	root := filepath.Join(dataDir, "demo-nginx")
	if _, err := os.Stat(root); err == nil {
		return
	}
	for _, d := range []string{"sites-available", "sites-enabled", "conf.d"} {
		_ = os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	write := func(name, content string, enable bool) {
		_ = os.WriteFile(filepath.Join(root, "sites-available", name), []byte(content), 0o644)
		if enable {
			_ = os.Symlink(filepath.Join("..", "sites-available", name), filepath.Join(root, "sites-enabled", name))
		}
	}
	write("shop.example.com", siteTemplate("proxy", "shop.example.com www.shop.example.com", "", "http://127.0.0.1:3000"), true)
	write("status.example.com", siteTemplate("proxy", "status.example.com", "", "http://127.0.0.1:3001"), true)
	write("blog.example.com", siteTemplate("static", "blog.example.com", "/var/www/blog", ""), true)
	write("old-landing", siteTemplate("static", "promo.example.com", "/var/www/promo", ""), false)
	_ = os.WriteFile(filepath.Join(root, "conf.d", "gzip.conf"), []byte("gzip on;\ngzip_types text/css application/javascript application/json image/svg+xml;\n"), 0o644)
}
