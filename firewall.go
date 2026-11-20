package main

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type FWRule struct {
	ID      string `json:"id"`
	Num     int    `json:"num,omitempty"`
	To      string `json:"to"`
	Action  string `json:"action"`
	From    string `json:"from"`
	Comment string `json:"comment,omitempty"`
	V6      bool   `json:"v6,omitempty"`
}

type FWState struct {
	Backend  string   `json:"backend"` // ufw | firewalld | iptables | demo | none
	Active   bool     `json:"active"`
	Default  string   `json:"default,omitempty"`
	Rules    []FWRule `json:"rules"`
	Note     string   `json:"note,omitempty"`
	ReadOnly bool     `json:"readOnly,omitempty"`
}

func detectFirewall() string {
	switch {
	case hasCmd("ufw"):
		return "ufw"
	case hasCmd("firewall-cmd"):
		return "firewalld"
	case hasCmd("iptables") && isLinux():
		return "iptables"
	case *flagDemo:
		return "demo"
	}
	return "none"
}

func isLinux() bool {
	_, err := os.Stat("/proc/self")
	return err == nil
}

var reUfwRule = regexp.MustCompile(`^\[\s*(\d+)\]\s+(.+?)\s{2,}((?:ALLOW|DENY|REJECT|LIMIT)(?: (?:IN|OUT|FWD))?)\s+(.+?)\s*(?:#\s*(.*))?$`)

func parseUfw(status, verbose string) FWState {
	st := FWState{Backend: "ufw", Rules: []FWRule{}}
	st.Active = strings.Contains(status, "Status: active")
	for _, l := range strings.Split(verbose, "\n") {
		if strings.HasPrefix(l, "Default:") {
			st.Default = strings.TrimSpace(strings.TrimPrefix(l, "Default:"))
		}
	}
	sc := bufio.NewScanner(strings.NewReader(status))
	for sc.Scan() {
		m := reUfwRule.FindStringSubmatch(strings.TrimRight(sc.Text(), " "))
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		to, from := strings.TrimSpace(m[2]), strings.TrimSpace(m[4])
		st.Rules = append(st.Rules, FWRule{ID: m[1], Num: n, To: to, Action: m[3], From: from,
			Comment: strings.TrimSpace(m[5]), V6: strings.Contains(to, "(v6)") || strings.Contains(from, "(v6)")})
	}
	return st
}

func getFirewall() FWState {
	switch b := detectFirewall(); b {
	case "ufw":
		out, err := runCmd(15*time.Second, "ufw", "status", "numbered")
		if err != nil {
			return FWState{Backend: b, Rules: []FWRule{}, Note: "Root privileges required to read ufw: " + firstLine(out)}
		}
		verbose, _ := runCmd(15*time.Second, "ufw", "status", "verbose")
		return parseUfw(out, verbose)
	case "firewalld":
		return getFirewalld()
	case "iptables":
		return getIptables()
	case "demo":
		return demoFirewall()
	}
	return FWState{Backend: "none", Rules: []FWRule{}, Note: "No supported firewall (ufw / firewalld / iptables) found on this machine."}
}

func getFirewalld() FWState {
	st := FWState{Backend: "firewalld", Rules: []FWRule{}}
	s, _ := runCmd(10*time.Second, "firewall-cmd", "--state")
	st.Active = strings.TrimSpace(s) == "running"
	if !st.Active {
		return st
	}
	out, err := runCmd(15*time.Second, "firewall-cmd", "--list-all")
	if err != nil {
		st.Note = firstLine(out)
		return st
	}
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch k {
		case "target":
			st.Default = "target " + v
		case "services":
			for _, s := range strings.Fields(v) {
				st.Rules = append(st.Rules, FWRule{ID: "service:" + s, To: s + " (service)", Action: "ALLOW IN", From: "Anywhere"})
			}
		case "ports":
			for _, p := range strings.Fields(v) {
				st.Rules = append(st.Rules, FWRule{ID: "port:" + p, To: p, Action: "ALLOW IN", From: "Anywhere"})
			}
		}
	}
	rich, _ := runCmd(10*time.Second, "firewall-cmd", "--list-rich-rules")
	for _, r := range strings.Split(strings.TrimSpace(rich), "\n") {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		act := "ALLOW IN"
		if strings.Contains(r, " reject") || strings.Contains(r, " drop") {
			act = "DENY IN"
		}
		from := "Anywhere"
		if m := regexp.MustCompile(`source address="?([^" ]+)`).FindStringSubmatch(r); m != nil {
			from = m[1]
		}
		to := "Anywhere"
		if m := regexp.MustCompile(`port port="?([^" ]+)"? protocol="?(\w+)`).FindStringSubmatch(r); m != nil {
			to = m[1] + "/" + m[2]
		}
		st.Rules = append(st.Rules, FWRule{ID: "rich:" + r, To: to, Action: act, From: from, Comment: "rich rule"})
	}
	return st
}

func getIptables() FWState {
	st := FWState{Backend: "iptables", Rules: []FWRule{}, Active: true,
		Note: "iptables changes made here will not persist across reboots unless iptables-persistent is installed."}
	out, err := runCmd(10*time.Second, "iptables", "-S", "INPUT")
	if err != nil {
		st.Note = "Root privileges required: " + firstLine(out)
		return st
	}
	n := 0
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "-P INPUT") {
			st.Default = "INPUT " + strings.TrimPrefix(l, "-P INPUT ")
		}
		if !strings.HasPrefix(l, "-A INPUT") {
			continue
		}
		n++
		f := splitArgs(l)
		r := FWRule{ID: l, Num: n, To: "Anywhere", From: "Anywhere"}
		for i := 0; i < len(f)-1; i++ {
			switch f[i] {
			case "-s":
				r.From = f[i+1]
			case "--dport":
				r.To = f[i+1]
			case "-j":
				r.Action = map[string]string{"ACCEPT": "ALLOW IN", "DROP": "DENY IN", "REJECT": "REJECT IN"}[f[i+1]]
				if r.Action == "" {
					r.Action = f[i+1]
				}
			case "--comment":
				r.Comment = f[i+1]
			}
		}
		if i := indexOf(f, "--dport"); i > 0 {
			if p := indexOf(f, "-p"); p > 0 {
				r.To = f[i+1] + "/" + f[p+1]
			}
		}
		st.Rules = append(st.Rules, r)
	}
	return st
}

func indexOf(a []string, s string) int {
	for i, x := range a {
		if x == s {
			return i
		}
	}
	return -1
}

func splitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	inQ := false
	for _, c := range s {
		switch {
		case c == '"':
			inQ = !inQ
		case c == ' ' && !inQ:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(c)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func handleFirewall(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, getFirewall())
}

// ---------- mutations ----------

type fwReq struct {
	Op      string `json:"op"` // enable | disable | add | delete | default
	Action  string `json:"action"`
	Port    string `json:"port"`
	Proto   string `json:"proto"`
	From    string `json:"from"`
	Comment string `json:"comment"`
	ID      string `json:"id"`
	Policy  string `json:"policy"`
}

var (
	rePort    = regexp.MustCompile(`^\d{1,5}(:\d{1,5})?$`)
	reComment = regexp.MustCompile(`^[\p{L}\p{N} ._:/-]{0,60}$`)
	fwMu      sync.Mutex
)

func validFrom(s string) bool {
	if s == "" || s == "any" {
		return true
	}
	if net.ParseIP(s) != nil {
		return true
	}
	_, _, err := net.ParseCIDR(s)
	return err == nil
}

func (q *fwReq) validate() error {
	if q.Op != "add" {
		return nil
	}
	q.Action = strings.ToLower(q.Action)
	if q.Action != "allow" && q.Action != "deny" && q.Action != "reject" && q.Action != "limit" {
		return fmt.Errorf("invalid action")
	}
	if q.Proto == "" {
		q.Proto = "any"
	}
	if q.Proto != "tcp" && q.Proto != "udp" && q.Proto != "any" {
		return fmt.Errorf("invalid protocol")
	}
	if q.Port != "" && !rePort.MatchString(q.Port) {
		return fmt.Errorf("invalid port (e.g. 80 or 8000:8100)")
	}
	if strings.Contains(q.Port, ":") && q.Proto == "any" {
		return fmt.Errorf("port range requires tcp or udp")
	}
	if !validFrom(q.From) {
		return fmt.Errorf("invalid source address")
	}
	if q.Port == "" && (q.From == "" || q.From == "any") {
		return fmt.Errorf("port or source address is required")
	}
	if !reComment.MatchString(q.Comment) {
		return fmt.Errorf("comment may only contain letters, numbers, and spaces (max 60)")
	}
	return nil
}

func sshPort() string {
	b, err := os.ReadFile("/etc/ssh/sshd_config")
	if err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			f := strings.Fields(l)
			if len(f) == 2 && strings.EqualFold(f[0], "Port") && rePort.MatchString(f[1]) {
				return f[1]
			}
		}
	}
	return "22"
}

func panelPort() string {
	_, p, err := net.SplitHostPort(*flagAddr)
	if err != nil || p == "" {
		return "1611"
	}
	return p
}

func handleFirewallAction(w http.ResponseWriter, r *http.Request) {
	var q fwReq
	if err := readJSON(r, &q); err != nil {
		httpErr(w, 400, "bad request")
		return
	}
	if err := q.validate(); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	fwMu.Lock()
	defer fwMu.Unlock()
	var out string
	var err error
	switch detectFirewall() {
	case "ufw":
		out, err = ufwAction(q)
	case "firewalld":
		out, err = firewalldAction(q)
	case "iptables":
		out, err = iptablesAction(q)
	case "demo":
		out, err = demoFirewallAction(q)
	default:
		httpErr(w, 400, "no supported firewall backend")
		return
	}
	if err != nil {
		httpErr(w, 500, strings.TrimSpace(out+" "+err.Error()))
		return
	}
	writeJSON(w, map[string]any{"ok": true, "output": strings.TrimSpace(out)})
}

func ufwAction(q fwReq) (string, error) {
	run := func(a ...string) (string, error) { return runCmd(30*time.Second, "ufw", a...) }
	switch q.Op {
	case "enable":
		// never lock the admin out: keep SSH and the panel reachable
		_, _ = run("allow", sshPort()+"/tcp", "comment", "ssh (tns safeguard)")
		_, _ = run("allow", panelPort()+"/tcp", "comment", "tns panel")
		return run("--force", "enable")
	case "disable":
		return run("disable")
	case "default":
		if q.Policy != "allow" && q.Policy != "deny" {
			return "", fmt.Errorf("invalid policy")
		}
		return run("default", q.Policy, "incoming")
	case "delete":
		n, err := strconv.Atoi(q.ID)
		if err != nil || n < 1 {
			return "", fmt.Errorf("invalid rule ID")
		}
		return run("--force", "delete", strconv.Itoa(n))
	case "add":
		var args []string
		from := q.From
		if from == "" {
			from = "any"
		}
		// deny rules for a specific source go first so they win over allows
		if (q.Action == "deny" || q.Action == "reject") && from != "any" && !strings.Contains(from, ":") {
			if st, _ := runCmd(10*time.Second, "ufw", "status", "numbered"); reUfwRule.MatchString(firstRuleLine(st)) {
				args = append(args, "insert", "1")
			}
		}
		args = append(args, q.Action)
		if q.Port == "" {
			args = append(args, "from", from)
		} else {
			if q.Proto != "any" {
				args = append(args, "proto", q.Proto)
			}
			args = append(args, "from", from, "to", "any", "port", q.Port)
		}
		if q.Comment != "" {
			args = append(args, "comment", q.Comment)
		}
		return run(args...)
	}
	return "", fmt.Errorf("invalid operation")
}

func firstRuleLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "[") {
			return strings.TrimRight(l, " ")
		}
	}
	return ""
}

func firewalldAction(q fwReq) (string, error) {
	run := func(a ...string) (string, error) { return runCmd(30*time.Second, "firewall-cmd", a...) }
	reload := func(out string, err error) (string, error) {
		if err != nil {
			return out, err
		}
		o2, err := run("--reload")
		return out + o2, err
	}
	switch q.Op {
	case "enable":
		return runCmd(30*time.Second, "systemctl", "enable", "--now", "firewalld")
	case "disable":
		return runCmd(30*time.Second, "systemctl", "disable", "--now", "firewalld")
	case "delete":
		kind, val, _ := strings.Cut(q.ID, ":")
		switch kind {
		case "service":
			return reload(run("--permanent", "--remove-service="+val))
		case "port":
			return reload(run("--permanent", "--remove-port="+val))
		case "rich":
			return reload(run("--permanent", "--remove-rich-rule="+val))
		}
		return "", fmt.Errorf("invalid rule ID")
	case "add":
		from := q.From
		if (from == "" || from == "any") && q.Action == "allow" && q.Proto != "any" {
			return reload(run("--permanent", "--add-port="+strings.Replace(q.Port, ":", "-", 1)+"/"+q.Proto))
		}
		fam := "ipv4"
		if strings.Contains(from, ":") {
			fam = "ipv6"
		}
		rule := "rule family=\"" + fam + "\""
		if from != "" && from != "any" {
			rule += " source address=\"" + from + "\""
		}
		if q.Port != "" {
			protos := []string{q.Proto}
			if q.Proto == "any" {
				protos = []string{"tcp"}
			}
			rule += " port port=\"" + strings.Replace(q.Port, ":", "-", 1) + "\" protocol=\"" + protos[0] + "\""
		}
		act := map[string]string{"allow": "accept", "deny": "drop", "reject": "reject", "limit": "accept limit value=\"6/m\""}[q.Action]
		rule += " " + act
		return reload(run("--permanent", "--add-rich-rule="+rule))
	}
	return "", fmt.Errorf("invalid operation for firewalld")
}

func iptablesAction(q fwReq) (string, error) {
	switch q.Op {
	case "delete":
		if !strings.HasPrefix(q.ID, "-A INPUT ") {
			return "", fmt.Errorf("invalid rule ID")
		}
		args := append([]string{"-D"}, splitArgs(strings.TrimPrefix(q.ID, "-A "))...)
		return runCmd(15*time.Second, "iptables", args...)
	case "add":
		args := []string{"-I", "INPUT"}
		if q.Action == "allow" {
			args = []string{"-A", "INPUT"}
		}
		if q.From != "" && q.From != "any" {
			if strings.Contains(q.From, ":") {
				return "", fmt.Errorf("iptables backend only supports IPv4")
			}
			args = append(args, "-s", q.From)
		}
		if q.Port != "" {
			p := q.Proto
			if p == "any" {
				p = "tcp"
			}
			args = append(args, "-p", p, "--dport", q.Port)
		}
		j := map[string]string{"allow": "ACCEPT", "deny": "DROP", "reject": "REJECT", "limit": "ACCEPT"}[q.Action]
		if q.Comment != "" {
			args = append(args, "-m", "comment", "--comment", q.Comment)
		}
		args = append(args, "-j", j)
		return runCmd(15*time.Second, "iptables", args...)
	}
	return "", fmt.Errorf("iptables backend only supports add/delete rules")
}
