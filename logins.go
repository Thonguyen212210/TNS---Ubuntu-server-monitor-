package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type LoginEvent struct {
	T       time.Time `json:"t"`
	IP      string    `json:"ip"`
	User    string    `json:"user"`
	OK      bool      `json:"ok"`
	Method  string    `json:"method"`
	Service string    `json:"service"` // ssh | panel
}

type Geo struct {
	Country string  `json:"country"`
	CC      string  `json:"cc"`
	City    string  `json:"city"`
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
	Local   bool    `json:"local,omitempty"`
}

const loginWindow = 30 * 24 * time.Hour

var (
	reTS       = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}T\S+|[A-Z][a-z]{2}\s+\d{1,2} \d{2}:\d{2}:\d{2})\s`)
	reAccepted = regexp.MustCompile(`Accepted (\S+) for (\S+) from (\S+) port`)
	reFailed   = regexp.MustCompile(`Failed (\S+) for (\S+) from (\S+) port`)
	reInvalid  = regexp.MustCompile(`Invalid user (\S*) from (\S+)`)
	reClosed   = regexp.MustCompile(`Connection closed by authenticating user (\S+) (\S+) port`)
)

var (
	loginMu     sync.Mutex
	loginCache  []LoginEvent
	loginSource string
	loginNote   string
	loginAt     time.Time
	panelEvents []LoginEvent

	geoMu    sync.Mutex
	geoCache = map[string]Geo{}
	geoQueue = map[string]bool{}
	geoKick  = make(chan struct{}, 1)
)

func startLogins() {
	loadGeoCache()
	if b, err := os.ReadFile(filepath.Join(dataDir, "panel_logins.json")); err == nil {
		_ = json.Unmarshal(b, &panelEvents)
	}
	if !*flagNoGeo {
		go geoWorker()
	}
}

func recordPanelLogin(ip string, ok bool) {
	loginMu.Lock()
	defer loginMu.Unlock()
	panelEvents = append(panelEvents, LoginEvent{T: time.Now(), IP: ip, OK: ok, User: "admin", Method: "password", Service: "panel"})
	cut := time.Now().Add(-loginWindow)
	i := 0
	for i < len(panelEvents) && panelEvents[i].T.Before(cut) {
		i++
	}
	panelEvents = panelEvents[i:]
	if len(panelEvents) > 5000 {
		panelEvents = panelEvents[len(panelEvents)-5000:]
	}
	b, _ := json.Marshal(panelEvents)
	_ = os.WriteFile(filepath.Join(dataDir, "panel_logins.json"), b, 0o600)
}

// parseTS parses syslog ("Nov 18 12:00:01") or ISO timestamps.
func parseTS(s string, now time.Time) (time.Time, bool) {
	if len(s) > 0 && s[0] >= '0' && s[0] <= '9' {
		for _, l := range []string{time.RFC3339Nano, "2006-01-02T15:04:05-0700", "2006-01-02T15:04:05.999999-0700"} {
			if t, err := time.Parse(l, s); err == nil {
				return t, true
			}
		}
		return time.Time{}, false
	}
	s = strings.Join(strings.Fields(s), " ")
	t, err := time.ParseInLocation("Jan 2 15:04:05", s, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	t = t.AddDate(now.Year(), 0, 0)
	if t.After(now.Add(24 * time.Hour)) {
		t = t.AddDate(-1, 0, 0)
	}
	return t, true
}

func parseAuthLine(line string, now time.Time) (LoginEvent, bool) {
	if !strings.Contains(line, "sshd") {
		return LoginEvent{}, false
	}
	m := reTS.FindStringSubmatch(line)
	if m == nil {
		return LoginEvent{}, false
	}
	t, ok := parseTS(m[1], now)
	if !ok {
		return LoginEvent{}, false
	}
	ev := LoginEvent{T: t, Service: "ssh"}
	if x := reAccepted.FindStringSubmatch(line); x != nil {
		ev.OK, ev.Method, ev.User, ev.IP = true, x[1], x[2], x[3]
	} else if x := reFailed.FindStringSubmatch(line); x != nil {
		if x[2] == "invalid" { // "Failed password for invalid user X" — counted via "Invalid user" line
			return ev, false
		}
		ev.Method, ev.User, ev.IP = x[1], x[2], x[3]
	} else if x := reInvalid.FindStringSubmatch(line); x != nil {
		ev.Method, ev.User, ev.IP = "invalid-user", x[1], x[2]
	} else if x := reClosed.FindStringSubmatch(line); x != nil {
		ev.Method, ev.User, ev.IP = "preauth", x[1], x[2]
	} else {
		return ev, false
	}
	if net.ParseIP(ev.IP) == nil {
		return ev, false
	}
	return ev, true
}

func readMaybeGz(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		r = gz
	}
	return io.ReadAll(io.LimitReader(r, 200<<20))
}

func collectSSHEvents() ([]LoginEvent, string, string) {
	now := time.Now()
	cut := now.Add(-loginWindow)
	var evs []LoginEvent
	parse := func(data []byte) {
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			if ev, ok := parseAuthLine(sc.Text(), now); ok && ev.T.After(cut) {
				evs = append(evs, ev)
			}
		}
	}
	for _, base := range []string{"/var/log/auth.log", "/var/log/secure"} {
		if _, err := os.Stat(base); err != nil {
			continue
		}
		files := []string{base, base + ".1"}
		for i := 2; i <= 5; i++ {
			files = append(files, base+"."+string(rune('0'+i))+".gz")
		}
		if m, _ := filepath.Glob(base + "-*"); len(m) > 0 { // RHEL: secure-20261101
			files = append(files, m...)
		}
		readAny := false
		for _, f := range files {
			if b, err := readMaybeGz(f); err == nil {
				readAny = true
				parse(b)
			}
		}
		if readAny {
			return evs, filepath.Base(base), ""
		}
		return nil, "none", "Không đọc được " + base + " — hãy chạy TNS bằng root (hoặc thêm user vào nhóm adm)."
	}
	if hasCmd("journalctl") {
		out, err := runCmd(60*time.Second, "journalctl", "-t", "sshd", "-t", "sshd-session", "-o", "short-iso", "--no-pager", "--since", "-30d")
		if err == nil || len(out) > 0 {
			parse([]byte(out))
			return evs, "journald", ""
		}
	}
	return nil, "none", "Không tìm thấy log SSH (auth.log / secure / journald) trên máy này."
}

func getLoginEvents() ([]LoginEvent, string, string) {
	loginMu.Lock()
	defer loginMu.Unlock()
	if time.Since(loginAt) > 60*time.Second {
		evs, src, note := collectSSHEvents()
		if len(evs) == 0 && *flagDemo {
			evs, src, note = demoLoginEvents(), "demo", "Dữ liệu minh hoạ (chế độ demo) — trên server Linux thật sẽ đọc từ auth.log/journald."
		}
		loginCache, loginSource, loginNote, loginAt = evs, src, note, time.Now()
	}
	all := make([]LoginEvent, 0, len(loginCache)+len(panelEvents))
	all = append(all, loginCache...)
	all = append(all, panelEvents...)
	return all, loginSource, loginNote
}

// ---------- geolocation ----------

func geoPath() string { return filepath.Join(dataDir, "geo.json") }

func loadGeoCache() {
	if b, err := os.ReadFile(geoPath()); err == nil {
		_ = json.Unmarshal(b, &geoCache)
	}
}

func isLocalIP(ip net.IP) bool {
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
}

func lookupGeo(ipStr string) (Geo, bool) {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return Geo{}, false
	}
	if isLocalIP(ip) {
		return Geo{Country: "Mạng nội bộ", CC: "LAN", Local: true}, true
	}
	if g, ok := demoGeo(ipStr); ok {
		return g, true
	}
	geoMu.Lock()
	defer geoMu.Unlock()
	if g, ok := geoCache[ipStr]; ok {
		return g, true
	}
	if !*flagNoGeo && !geoQueue[ipStr] {
		geoQueue[ipStr] = true
		select {
		case geoKick <- struct{}{}:
		default:
		}
	}
	return Geo{}, false
}

// geoWorker resolves queued IPs via ip-api.com batch endpoint (≤100 IPs/request, ≤15 requests/min).
func geoWorker() {
	client := &http.Client{Timeout: 15 * time.Second}
	for range geoKick {
		for {
			geoMu.Lock()
			batch := make([]string, 0, 100)
			for ip := range geoQueue {
				batch = append(batch, ip)
				if len(batch) == 100 {
					break
				}
			}
			geoMu.Unlock()
			if len(batch) == 0 {
				break
			}
			body, _ := json.Marshal(batch)
			resp, err := client.Post("http://ip-api.com/batch?fields=status,country,countryCode,city,lat,lon,query", "application/json", bytes.NewReader(body))
			if err != nil {
				log.Printf("geo: %v", err)
				time.Sleep(30 * time.Second)
				continue
			}
			var res []struct {
				Status, Country, CountryCode, City, Query string
				Lat, Lon                                  float64
			}
			_ = json.NewDecoder(resp.Body).Decode(&res)
			resp.Body.Close()
			geoMu.Lock()
			for _, r := range res {
				g := Geo{Country: r.Country, CC: r.CountryCode, City: r.City, Lat: r.Lat, Lon: r.Lon}
				if r.Status != "success" {
					g = Geo{Country: "Không rõ", CC: "??"}
				}
				geoCache[r.Query] = g
				delete(geoQueue, r.Query)
			}
			for _, ip := range batch { // never retry forever
				delete(geoQueue, ip)
			}
			b, _ := json.Marshal(geoCache)
			geoMu.Unlock()
			_ = os.WriteFile(geoPath(), b, 0o600)
			time.Sleep(4500 * time.Millisecond)
		}
	}
}

// ---------- aggregation ----------

type ipAgg struct {
	IP      string    `json:"ip"`
	Geo     *Geo      `json:"geo,omitempty"`
	Success int       `json:"success"`
	Failed  int       `json:"failed"`
	Users   []string  `json:"users"`
	First   time.Time `json:"first"`
	Last    time.Time `json:"last"`
	LastOK  time.Time `json:"lastOk,omitempty"`
	Service string    `json:"service"`
	users   map[string]bool
}

type alert struct {
	Level  string    `json:"level"` // crit | warn | info
	Title  string    `json:"title"`
	Detail string    `json:"detail"`
	IP     string    `json:"ip,omitempty"`
	T      time.Time `json:"t"`
}

func handleLogins(w http.ResponseWriter, r *http.Request) {
	evs, src, note := getLoginEvents()
	sort.Slice(evs, func(i, j int) bool { return evs[i].T.Before(evs[j].T) })
	now := time.Now()

	byIP := map[string]*ipAgg{}
	type ccAgg struct {
		CC      string `json:"cc"`
		Country string `json:"country"`
		Success int    `json:"success"`
		Failed  int    `json:"failed"`
		IPs     int    `json:"ips"`
	}
	byCC := map[string]*ccAgg{}
	hours := 7 * 24
	start := now.Truncate(time.Hour).Add(-time.Duration(hours-1) * time.Hour)
	hourly := make([][2]int, hours)
	totals := map[string]int{}
	okCountries := map[string][]time.Time{}
	var alerts []alert

	for _, e := range evs {
		a := byIP[e.IP]
		if a == nil {
			a = &ipAgg{IP: e.IP, First: e.T, users: map[string]bool{}, Service: e.Service}
			if g, ok := lookupGeo(e.IP); ok {
				gg := g
				a.Geo = &gg
			}
			byIP[e.IP] = a
		}
		a.Last = e.T
		if e.Service != a.Service {
			a.Service = "ssh+panel"
		}
		if len(a.users) < 20 && e.User != "" {
			a.users[e.User] = true
		}
		if e.OK {
			a.Success++
			a.LastOK = e.T
			totals["success"]++
			if e.Service == "ssh" && a.Failed >= 5 {
				alerts = append(alerts, alert{Level: "crit", T: e.T, IP: e.IP,
					Title:  "Đăng nhập thành công sau tấn công dò mật khẩu",
					Detail: "IP " + e.IP + " đã thất bại " + itoa(a.Failed) + " lần rồi đăng nhập thành công vào user '" + e.User + "'."})
			}
			if e.Service == "ssh" && e.User == "root" && e.Method == "password" {
				alerts = append(alerts, alert{Level: "warn", T: e.T, IP: e.IP, Title: "Root đăng nhập bằng mật khẩu",
					Detail: "Nên tắt PasswordAuthentication / PermitRootLogin trong sshd_config."})
			}
			if a.Geo != nil && !a.Geo.Local {
				okCountries[a.Geo.CC] = append(okCountries[a.Geo.CC], e.T)
			}
		} else {
			a.Failed++
			totals["failed"]++
		}
		if a.Geo != nil {
			c := byCC[a.Geo.CC]
			if c == nil {
				c = &ccAgg{CC: a.Geo.CC, Country: a.Geo.Country}
				byCC[a.Geo.CC] = c
			}
			if e.OK {
				c.Success++
			} else {
				c.Failed++
			}
		}
		if idx := int(e.T.Sub(start) / time.Hour); idx >= 0 && idx < hours {
			if e.OK {
				hourly[idx][0]++
			} else {
				hourly[idx][1]++
			}
		}
	}

	// successful logins from a country seen only once, in the last 72h
	for cc, ts := range okCountries {
		if len(ts) == 1 && now.Sub(ts[0]) < 72*time.Hour {
			name := cc
			if c := byCC[cc]; c != nil {
				name = c.Country
			}
			alerts = append(alerts, alert{Level: "warn", T: ts[0], Title: "Đăng nhập từ quốc gia mới: " + name,
				Detail: "Lần đầu tiên có đăng nhập thành công từ " + name + " trong 30 ngày qua."})
		}
	}

	ips := make([]*ipAgg, 0, len(byIP))
	for _, a := range byIP {
		for u := range a.users {
			a.Users = append(a.Users, u)
		}
		sort.Strings(a.Users)
		if a.Geo != nil {
			if c := byCC[a.Geo.CC]; c != nil {
				c.IPs++
			}
		}
		if a.Failed >= 50 && a.Success == 0 && now.Sub(a.Last) < 24*time.Hour {
			alerts = append(alerts, alert{Level: "info", T: a.Last, IP: a.IP, Title: "Dò mật khẩu: " + a.IP,
				Detail: itoa(a.Failed) + " lần thất bại. Có thể chặn IP này bằng tường lửa."})
		}
		ips = append(ips, a)
	}
	sort.Slice(ips, func(i, j int) bool {
		ti, tj := ips[i].Success*1000+ips[i].Failed, ips[j].Success*1000+ips[j].Failed
		if ips[i].Success > 0 != (ips[j].Success > 0) {
			return ips[i].Success > 0
		}
		return ti > tj
	})
	ccs := make([]*ccAgg, 0, len(byCC))
	for _, c := range byCC {
		ccs = append(ccs, c)
	}
	sort.Slice(ccs, func(i, j int) bool { return ccs[i].Success+ccs[i].Failed > ccs[j].Success+ccs[j].Failed })
	sort.Slice(alerts, func(i, j int) bool {
		li, lj := levelRank(alerts[i].Level), levelRank(alerts[j].Level)
		if li != lj {
			return li < lj
		}
		return alerts[i].T.After(alerts[j].T)
	})
	if len(alerts) > 30 {
		alerts = alerts[:30]
	}

	type recentEv struct {
		LoginEvent
		Geo *Geo `json:"geo,omitempty"`
	}
	var recent []recentEv
	for i := len(evs) - 1; i >= 0 && len(recent) < 150; i-- {
		re := recentEv{LoginEvent: evs[i]}
		if a := byIP[evs[i].IP]; a != nil {
			re.Geo = a.Geo
		}
		recent = append(recent, re)
	}
	if len(ips) > 1500 {
		ips = ips[:1500]
	}
	geoMode := "ip-api.com"
	if *flagNoGeo {
		geoMode = "off"
	}
	writeJSON(w, map[string]any{
		"source": src, "note": note, "geo": geoMode, "days": 30,
		"totals": map[string]int{"attempts": totals["success"] + totals["failed"], "success": totals["success"],
			"failed": totals["failed"], "ips": len(byIP), "countries": len(byCC)},
		"ips": ips, "countries": ccs, "recent": recent, "alerts": alerts,
		"hourly": hourly, "hourlyStart": start.UnixMilli(),
	})
}

func levelRank(l string) int {
	switch l {
	case "crit":
		return 0
	case "warn":
		return 1
	}
	return 2
}

func itoa(i int) string { return strconv.Itoa(i) }
