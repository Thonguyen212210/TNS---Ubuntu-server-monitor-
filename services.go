package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ================= systemd services =================

type Service struct {
	Unit    string `json:"unit"`
	Load    string `json:"load"`
	Active  string `json:"active"`
	Sub     string `json:"sub"`
	Desc    string `json:"desc"`
	Enabled string `json:"enabled"`
}

var reUnit = regexp.MustCompile(`^[a-zA-Z0-9@._:\\-]+\.service$`)

func handleServices(w http.ResponseWriter, r *http.Request) {
	if !hasCmd("systemctl") {
		if *flagDemo {
			writeJSON(w, map[string]any{"available": true, "services": demoServices(), "demo": true})
			return
		}
		writeJSON(w, map[string]any{"available": false, "note": "Máy này không dùng systemd."})
		return
	}
	out, err := runCmd(20*time.Second, "systemctl", "list-units", "--type=service", "--all", "--no-pager", "--no-legend", "--plain")
	if err != nil {
		writeJSON(w, map[string]any{"available": false, "note": firstLine(out)})
		return
	}
	enabled := map[string]string{}
	if uf, err := runCmd(20*time.Second, "systemctl", "list-unit-files", "--type=service", "--no-pager", "--no-legend"); err == nil {
		for _, l := range strings.Split(uf, "\n") {
			f := strings.Fields(l)
			if len(f) >= 2 {
				enabled[f[0]] = f[1]
			}
		}
	}
	var svcs []Service
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) < 4 || !strings.HasSuffix(f[0], ".service") {
			continue
		}
		if f[1] == "not-found" {
			continue
		}
		svcs = append(svcs, Service{Unit: f[0], Load: f[1], Active: f[2], Sub: f[3],
			Desc: strings.Join(f[4:], " "), Enabled: enabled[f[0]]})
	}
	sort.Slice(svcs, func(i, j int) bool {
		ri, rj := activeRank(svcs[i].Active), activeRank(svcs[j].Active)
		if ri != rj {
			return ri < rj
		}
		return svcs[i].Unit < svcs[j].Unit
	})
	writeJSON(w, map[string]any{"available": true, "services": svcs})
}

func activeRank(a string) int {
	switch a {
	case "failed":
		return 0
	case "active":
		return 1
	case "activating", "reloading", "deactivating":
		return 2
	}
	return 3
}

func normUnit(u string) string {
	if !strings.HasSuffix(u, ".service") {
		u += ".service"
	}
	return u
}

func handleServiceAction(w http.ResponseWriter, r *http.Request) {
	var q struct{ Unit, Op string }
	if err := readJSON(r, &q); err != nil {
		httpErr(w, 400, "bad request")
		return
	}
	q.Unit = normUnit(q.Unit)
	if !reUnit.MatchString(q.Unit) {
		httpErr(w, 400, "tên service không hợp lệ")
		return
	}
	switch q.Op {
	case "start", "stop", "restart", "reload", "enable", "disable":
	default:
		httpErr(w, 400, "op không hợp lệ")
		return
	}
	if !hasCmd("systemctl") && *flagDemo {
		demoServiceAction(q.Unit, q.Op)
		writeJSON(w, map[string]bool{"ok": true})
		return
	}
	out, err := runCmd(60*time.Second, "systemctl", q.Op, q.Unit)
	if err != nil {
		httpErr(w, 500, strings.TrimSpace(out+" "+err.Error()))
		return
	}
	writeJSON(w, map[string]any{"ok": true, "output": out})
}

func handleServiceLogs(w http.ResponseWriter, r *http.Request) {
	u := normUnit(r.URL.Query().Get("unit"))
	if !reUnit.MatchString(u) {
		httpErr(w, 400, "tên service không hợp lệ")
		return
	}
	if !hasCmd("journalctl") {
		writeJSON(w, map[string]string{"logs": demoServiceLogs(u)})
		return
	}
	out, _ := runCmd(20*time.Second, "journalctl", "-u", u, "-n", "300", "--no-pager", "-o", "short-iso")
	st, _ := runCmd(10*time.Second, "systemctl", "status", u, "--no-pager", "-n", "0")
	writeJSON(w, map[string]string{"logs": out, "status": st})
}

// ================= nginx sites =================

type Site struct {
	Name    string   `json:"name"`
	Kind    string   `json:"kind"` // sites | conf.d
	Enabled bool     `json:"enabled"`
	Domains []string `json:"domains"`
	Listen  []string `json:"listen"`
	Root    string   `json:"root,omitempty"`
	Proxy   string   `json:"proxy,omitempty"`
	SSL     bool     `json:"ssl"`
}

var reSiteName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,100}$`)

func nginxRoot() string {
	if _, err := os.Stat("/etc/nginx"); err == nil {
		return "/etc/nginx"
	}
	if *flagDemo {
		return filepath.Join(dataDir, "demo-nginx")
	}
	return ""
}

func nginxDemo() bool { return *flagDemo && nginxRoot() != "/etc/nginx" }

func parseSite(content string) (domains, listen []string, root, proxy string, ssl bool) {
	for _, l := range strings.Split(content, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "#") {
			continue
		}
		l = strings.TrimSuffix(l, ";")
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "server_name":
			for _, d := range f[1:] {
				if d != "_" && !contains(domains, d) {
					domains = append(domains, d)
				}
			}
		case "listen":
			if !contains(listen, f[1]) {
				listen = append(listen, f[1])
			}
			if contains(f, "ssl") {
				ssl = true
			}
		case "root":
			if root == "" {
				root = f[1]
			}
		case "proxy_pass":
			if proxy == "" {
				proxy = f[1]
			}
		case "ssl_certificate":
			ssl = true
		}
	}
	return
}

func contains(a []string, s string) bool {
	for _, x := range a {
		if x == s {
			return true
		}
	}
	return false
}

func sitePath(kind, name string) (string, error) {
	root := nginxRoot()
	if root == "" || !reSiteName.MatchString(name) {
		return "", fmt.Errorf("tên site không hợp lệ")
	}
	switch kind {
	case "sites":
		return filepath.Join(root, "sites-available", name), nil
	case "conf.d":
		return filepath.Join(root, "conf.d", name), nil
	}
	return "", fmt.Errorf("loại không hợp lệ")
}

func listSites() []Site {
	root := nginxRoot()
	var sites []Site
	if ents, err := os.ReadDir(filepath.Join(root, "sites-available")); err == nil {
		for _, e := range ents {
			if e.IsDir() || strings.HasSuffix(e.Name(), "~") || strings.Contains(e.Name(), ".bak") {
				continue
			}
			b, _ := os.ReadFile(filepath.Join(root, "sites-available", e.Name()))
			_, err := os.Lstat(filepath.Join(root, "sites-enabled", e.Name()))
			s := Site{Name: e.Name(), Kind: "sites", Enabled: err == nil}
			s.Domains, s.Listen, s.Root, s.Proxy, s.SSL = parseSite(string(b))
			sites = append(sites, s)
		}
	}
	if ents, err := os.ReadDir(filepath.Join(root, "conf.d")); err == nil {
		for _, e := range ents {
			n := e.Name()
			if e.IsDir() || !(strings.HasSuffix(n, ".conf") || strings.HasSuffix(n, ".conf.disabled")) {
				continue
			}
			b, _ := os.ReadFile(filepath.Join(root, "conf.d", n))
			s := Site{Name: n, Kind: "conf.d", Enabled: strings.HasSuffix(n, ".conf")}
			s.Domains, s.Listen, s.Root, s.Proxy, s.SSL = parseSite(string(b))
			sites = append(sites, s)
		}
	}
	return sites
}

func nginxStatus() (bool, string) {
	if nginxDemo() {
		return true, "nginx/1.26.2 (demo)"
	}
	ver, _ := runCmd(5*time.Second, "nginx", "-v")
	running := false
	if hasCmd("systemctl") {
		s, _ := runCmd(5*time.Second, "systemctl", "is-active", "nginx")
		running = strings.TrimSpace(s) == "active"
	} else if _, err := os.Stat("/run/nginx.pid"); err == nil {
		running = true
	}
	return running, strings.TrimSpace(strings.TrimPrefix(firstLine(ver), "nginx version:"))
}

func handleSites(w http.ResponseWriter, r *http.Request) {
	if nginxRoot() == "" {
		writeJSON(w, map[string]any{"available": false, "note": "Chưa cài Nginx (/etc/nginx không tồn tại)."})
		return
	}
	running, ver := nginxStatus()
	writeJSON(w, map[string]any{"available": true, "running": running, "version": ver, "root": nginxRoot(),
		"sites": listSites(), "demo": nginxDemo()})
}

func handleSiteFile(w http.ResponseWriter, r *http.Request) {
	p, err := sitePath(r.URL.Query().Get("kind"), r.URL.Query().Get("name"))
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		httpErr(w, 404, err.Error())
		return
	}
	writeJSON(w, map[string]string{"content": string(b), "path": p})
}

func nginxTest() (string, error) {
	if nginxDemo() {
		return "nginx: configuration file test is successful (demo)", nil
	}
	return runCmd(20*time.Second, "nginx", "-t")
}

func nginxReload() (string, error) {
	if nginxDemo() {
		return "reloaded (demo)", nil
	}
	if hasCmd("systemctl") {
		if out, err := runCmd(20*time.Second, "systemctl", "reload", "nginx"); err == nil {
			return out, nil
		}
	}
	return runCmd(20*time.Second, "nginx", "-s", "reload")
}

func backupFile(p string) {
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	dir := filepath.Join(dataDir, "backups", "nginx")
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, filepath.Base(p)+"."+time.Now().Format("20060102-150405")), b, 0o600)
}

func siteTemplate(kind, domain, root, upstream string) string {
	if kind == "proxy" {
		return fmt.Sprintf(`server {
    listen 80;
    listen [::]:80;
    server_name %s;

    location / {
        proxy_pass %s;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
    }
}
`, domain, upstream)
	}
	return fmt.Sprintf(`server {
    listen 80;
    listen [::]:80;
    server_name %s;
    root %s;
    index index.html index.htm;

    location / {
        try_files $uri $uri/ =404;
    }
}
`, domain, root)
}

var (
	reDomain   = regexp.MustCompile(`^[a-zA-Z0-9*.-]{1,253}( [a-zA-Z0-9*.-]{1,253})*$`)
	reUpstream = regexp.MustCompile(`^https?://[a-zA-Z0-9.\-\[\]:]+(/[^\s;{}]*)?$`)
	reWebRoot  = regexp.MustCompile(`^/[^\s;{}]*$`)
)

func handleSiteAction(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Op, Name, Kind, Content          string
		Template, Domain, Root, Upstream string
	}
	if err := readJSON(r, &q); err != nil {
		httpErr(w, 400, "bad request")
		return
	}
	root := nginxRoot()
	if root == "" {
		httpErr(w, 400, "nginx chưa được cài")
		return
	}
	fail := func(code int, msg string) { httpErr(w, code, msg) }
	ok := func(out string) { writeJSON(w, map[string]any{"ok": true, "output": out}) }

	switch q.Op {
	case "test":
		out, err := nginxTest()
		if err != nil {
			fail(400, out)
			return
		}
		ok(out)
		return
	case "reload":
		out, err := nginxReload()
		if err != nil {
			fail(500, out)
			return
		}
		ok(out)
		return
	case "create":
		if !reSiteName.MatchString(q.Name) || !reDomain.MatchString(q.Domain) {
			fail(400, "tên site hoặc domain không hợp lệ")
			return
		}
		if q.Template == "proxy" && !reUpstream.MatchString(q.Upstream) {
			fail(400, "upstream phải dạng http://127.0.0.1:3000")
			return
		}
		if q.Template != "proxy" && !reWebRoot.MatchString(q.Root) {
			fail(400, "thư mục web không hợp lệ")
			return
		}
		kind, name := "sites", q.Name
		if _, err := os.Stat(filepath.Join(root, "sites-available")); err != nil {
			kind = "conf.d"
			if !strings.HasSuffix(name, ".conf") {
				name += ".conf"
			}
		}
		p, _ := sitePath(kind, name)
		if _, err := os.Stat(p); err == nil {
			fail(409, "site đã tồn tại")
			return
		}
		if err := os.WriteFile(p, []byte(siteTemplate(q.Template, q.Domain, q.Root, q.Upstream)), 0o644); err != nil {
			fail(500, err.Error())
			return
		}
		if kind == "sites" {
			_ = os.Symlink(filepath.Join("..", "sites-available", name), filepath.Join(root, "sites-enabled", name))
		}
		if out, err := nginxTest(); err != nil {
			_ = os.Remove(filepath.Join(root, "sites-enabled", name))
			_ = os.Remove(p)
			fail(400, "nginx -t lỗi, đã huỷ:\n"+out)
			return
		}
		out, _ := nginxReload()
		ok(out)
		return
	}

	p, err := sitePath(q.Kind, q.Name)
	if err != nil {
		fail(400, err.Error())
		return
	}
	if _, err := os.Stat(p); err != nil {
		fail(404, "không tìm thấy site")
		return
	}
	switch q.Op {
	case "save":
		old, _ := os.ReadFile(p)
		backupFile(p)
		if err := os.WriteFile(p, []byte(q.Content), 0o644); err != nil {
			fail(500, err.Error())
			return
		}
		enabled := q.Kind == "conf.d" && strings.HasSuffix(q.Name, ".conf")
		if q.Kind == "sites" {
			_, e := os.Lstat(filepath.Join(root, "sites-enabled", q.Name))
			enabled = e == nil
		}
		if enabled {
			if out, err := nginxTest(); err != nil {
				_ = os.WriteFile(p, old, 0o644)
				fail(400, "nginx -t lỗi — đã khôi phục bản cũ:\n"+out)
				return
			}
			out, _ := nginxReload()
			ok("Đã lưu và reload nginx.\n" + out)
			return
		}
		ok("Đã lưu (site đang tắt nên chưa reload).")
	case "enable", "disable":
		var undo func()
		if q.Kind == "sites" {
			link := filepath.Join(root, "sites-enabled", q.Name)
			if q.Op == "enable" {
				_ = os.Symlink(filepath.Join("..", "sites-available", q.Name), link)
				undo = func() { _ = os.Remove(link) }
			} else {
				_ = os.Remove(link)
				undo = func() { _ = os.Symlink(filepath.Join("..", "sites-available", q.Name), link) }
			}
		} else {
			var to string
			if q.Op == "enable" && strings.HasSuffix(q.Name, ".conf.disabled") {
				to = strings.TrimSuffix(p, ".disabled")
			} else if q.Op == "disable" && strings.HasSuffix(q.Name, ".conf") {
				to = p + ".disabled"
			}
			if to != "" {
				_ = os.Rename(p, to)
				undo = func() { _ = os.Rename(to, p) }
			}
		}
		if out, err := nginxTest(); err != nil {
			if undo != nil {
				undo()
			}
			fail(400, "nginx -t lỗi, đã hoàn tác:\n"+out)
			return
		}
		out, _ := nginxReload()
		ok(out)
	case "delete":
		backupFile(p)
		if q.Kind == "sites" {
			_ = os.Remove(filepath.Join(root, "sites-enabled", q.Name))
		}
		if err := os.Remove(p); err != nil {
			fail(500, err.Error())
			return
		}
		out, _ := nginxReload()
		ok(out)
	default:
		fail(400, "op không hợp lệ")
	}
}
