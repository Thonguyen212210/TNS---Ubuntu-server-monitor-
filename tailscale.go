package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

var reTSHost = regexp.MustCompile(`^[a-zA-Z0-9._:-]{1,128}$`)

type tsPeerOut struct {
	ID             string   `json:"id"`
	HostName       string   `json:"hostName"`
	DNSName        string   `json:"dnsName"`
	OS             string   `json:"os"`
	IPs            []string `json:"ips"`
	Online         bool     `json:"online"`
	Active         bool     `json:"active"`
	Direct         bool     `json:"direct"`
	CurAddr        string   `json:"curAddr,omitempty"`
	Relay          string   `json:"relay,omitempty"`
	RxBytes        int64    `json:"rxBytes"`
	TxBytes        int64    `json:"txBytes"`
	LastSeen       string   `json:"lastSeen,omitempty"`
	ExitNode       bool     `json:"exitNode"`
	ExitNodeOption bool     `json:"exitNodeOption"`
	PrimaryRoutes  []string `json:"primaryRoutes,omitempty"`
	Tags           []string `json:"tags,omitempty"`
}

func handleTailscaleStatus(w http.ResponseWriter, r *http.Request) {
	if !hasCmd("tailscale") {
		writeJSON(w, map[string]any{
			"available": false,
			"note":      "Tailscale is not installed on this server.",
		})
		return
	}

	out, err := runCmd(10*time.Second, "tailscale", "status", "--json")
	if err != nil && strings.TrimSpace(out) == "" {
		writeJSON(w, map[string]any{
			"available": false,
			"note":      "Failed to read Tailscale status: " + err.Error(),
		})
		return
	}

	var raw struct {
		Version        string   `json:"Version"`
		BackendState   string   `json:"BackendState"`
		AuthURL        string   `json:"AuthURL"`
		Health         []string `json:"Health"`
		MagicDNSSuffix string   `json:"MagicDNSSuffix"`
		CurrentTailnet *struct {
			Name            string `json:"Name"`
			MagicDNSSuffix  string `json:"MagicDNSSuffix"`
			MagicDNSEnabled bool   `json:"MagicDNSEnabled"`
		} `json:"CurrentTailnet"`
		Self *struct {
			ID             string   `json:"ID"`
			HostName       string   `json:"HostName"`
			DNSName        string   `json:"DNSName"`
			OS             string   `json:"OS"`
			TailscaleIPs   []string `json:"TailscaleIPs"`
			AllowedIPs     []string `json:"AllowedIPs"`
			PrimaryRoutes  []string `json:"PrimaryRoutes"`
			Online         bool     `json:"Online"`
			Relay          string   `json:"Relay"`
			CurAddr        string   `json:"CurAddr"`
			ExitNode       bool     `json:"ExitNode"`
			ExitNodeOption bool     `json:"ExitNodeOption"`
			KeyExpiry      string   `json:"KeyExpiry"`
		} `json:"Self"`
		Peer map[string]struct {
			ID             string   `json:"ID"`
			HostName       string   `json:"HostName"`
			DNSName        string   `json:"DNSName"`
			OS             string   `json:"OS"`
			TailscaleIPs   []string `json:"TailscaleIPs"`
			PrimaryRoutes  []string `json:"PrimaryRoutes"`
			Tags           []string `json:"Tags"`
			Online         bool     `json:"Online"`
			Active         bool     `json:"Active"`
			Relay          string   `json:"Relay"`
			CurAddr        string   `json:"CurAddr"`
			RxBytes        int64    `json:"RxBytes"`
			TxBytes        int64    `json:"TxBytes"`
			LastSeen       string   `json:"LastSeen"`
			ExitNode       bool     `json:"ExitNode"`
			ExitNodeOption bool     `json:"ExitNodeOption"`
		} `json:"Peer"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		writeJSON(w, map[string]any{
			"available": false,
			"note":      "Failed to parse Tailscale output: " + firstLine(out),
		})
		return
	}

	// Read live prefs via `tailscale debug prefs`
	prefsOut := map[string]any{
		"wantRunning":       true,
		"advertiseRoutes":   []string{},
		"advertiseExitNode": false,
		"acceptRoutes":      false,
		"acceptDns":         true,
		"runSsh":            false,
		"shieldsUp":         false,
		"exitNodeIp":        "",
		"exitNodeId":        "",
		"exitNodeAllowLan":  false,
		"noSnat":            false,
	}
	if pRaw, err := runCmd(8*time.Second, "tailscale", "debug", "prefs"); err == nil {
		var pr struct {
			WantRunning            bool     `json:"WantRunning"`
			RouteAll               bool     `json:"RouteAll"`
			CorpDNS                bool     `json:"CorpDNS"`
			RunSSH                 bool     `json:"RunSSH"`
			ShieldsUp              bool     `json:"ShieldsUp"`
			NoSNAT                 bool     `json:"NoSNAT"`
			ExitNodeID             string   `json:"ExitNodeID"`
			ExitNodeIP             string   `json:"ExitNodeIP"`
			ExitNodeAllowLANAccess bool     `json:"ExitNodeAllowLANAccess"`
			AdvertiseRoutes        []string `json:"AdvertiseRoutes"`
		}
		if json.Unmarshal([]byte(pRaw), &pr) == nil {
			advExit := false
			var subRoutes []string
			for _, rt := range pr.AdvertiseRoutes {
				if rt == "0.0.0.0/0" || rt == "::/0" {
					advExit = true
				} else {
					subRoutes = append(subRoutes, rt)
				}
			}
			if subRoutes == nil {
				subRoutes = []string{}
			}
			prefsOut["wantRunning"] = pr.WantRunning
			prefsOut["advertiseRoutes"] = subRoutes
			prefsOut["advertiseExitNode"] = advExit
			prefsOut["acceptRoutes"] = pr.RouteAll
			prefsOut["acceptDns"] = pr.CorpDNS
			prefsOut["runSsh"] = pr.RunSSH
			prefsOut["shieldsUp"] = pr.ShieldsUp
			prefsOut["noSnat"] = pr.NoSNAT
			prefsOut["exitNodeId"] = pr.ExitNodeID
			prefsOut["exitNodeIp"] = pr.ExitNodeIP
			prefsOut["exitNodeAllowLan"] = pr.ExitNodeAllowLANAccess
		}
	}

	// Optional serve config
	var serveCfg any
	if sRaw, err := runCmd(5*time.Second, "tailscale", "serve", "status", "--json"); err == nil {
		_ = json.Unmarshal([]byte(sRaw), &serveCfg)
	}

	peers := make([]tsPeerOut, 0, len(raw.Peer))
	for _, p := range raw.Peer {
		ls := p.LastSeen
		if strings.HasPrefix(ls, "0001-") {
			ls = ""
		}
		peers = append(peers, tsPeerOut{
			ID:             p.ID,
			HostName:       p.HostName,
			DNSName:        strings.TrimSuffix(p.DNSName, "."),
			OS:             p.OS,
			IPs:            p.TailscaleIPs,
			Online:         p.Online,
			Active:         p.Active,
			Direct:         p.CurAddr != "",
			CurAddr:        p.CurAddr,
			Relay:          p.Relay,
			RxBytes:        p.RxBytes,
			TxBytes:        p.TxBytes,
			LastSeen:       ls,
			ExitNode:       p.ExitNode,
			ExitNodeOption: p.ExitNodeOption,
			PrimaryRoutes:  p.PrimaryRoutes,
			Tags:           p.Tags,
		})
	}
	sort.Slice(peers, func(i, j int) bool {
		if peers[i].Online != peers[j].Online {
			return peers[i].Online
		}
		if peers[i].Active != peers[j].Active {
			return peers[i].Active
		}
		return strings.ToLower(peers[i].HostName) < strings.ToLower(peers[j].HostName)
	})

	var selfOut map[string]any
	if raw.Self != nil {
		selfOut = map[string]any{
			"id":             raw.Self.ID,
			"hostName":       raw.Self.HostName,
			"dnsName":        strings.TrimSuffix(raw.Self.DNSName, "."),
			"os":             raw.Self.OS,
			"ips":            raw.Self.TailscaleIPs,
			"allowedIps":     raw.Self.AllowedIPs,
			"primaryRoutes":  raw.Self.PrimaryRoutes,
			"online":         raw.Self.Online,
			"relay":          raw.Self.Relay,
			"curAddr":        raw.Self.CurAddr,
			"exitNode":       raw.Self.ExitNode,
			"exitNodeOption": raw.Self.ExitNodeOption,
			"keyExpiry":      raw.Self.KeyExpiry,
		}
	}

	writeJSON(w, map[string]any{
		"available":      true,
		"version":        raw.Version,
		"backendState":   raw.BackendState,
		"authUrl":        raw.AuthURL,
		"health":         raw.Health,
		"magicDnsSuffix": raw.MagicDNSSuffix,
		"tailnet":        raw.CurrentTailnet,
		"self":           selfOut,
		"prefs":          prefsOut,
		"peers":          peers,
		"serve":          serveCfg,
	})
}

func validateRouteList(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	parts := strings.Split(raw, ",")
	var clean []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(p); err != nil {
			return "", fmt.Errorf("invalid CIDR: %s", p)
		}
		clean = append(clean, p)
	}
	return strings.Join(clean, ","), nil
}

func handleTailscaleAction(w http.ResponseWriter, r *http.Request) {
	if !hasCmd("tailscale") {
		httpErr(w, 400, "Tailscale is not installed on this server")
		return
	}
	var q struct {
		Op                string `json:"op"` // up | down | set | ping | netcheck | serve_reset
		Target            string `json:"target"`
		AdvertiseRoutes   *string `json:"advertiseRoutes"`
		AdvertiseExitNode *bool   `json:"advertiseExitNode"`
		AcceptRoutes      *bool   `json:"acceptRoutes"`
		AcceptDNS         *bool   `json:"acceptDns"`
		SSH               *bool   `json:"ssh"`
		ShieldsUp         *bool   `json:"shieldsUp"`
		ExitNode          *string `json:"exitNode"`
		ExitNodeAllowLAN  *bool   `json:"exitNodeAllowLan"`
	}
	if err := readJSON(r, &q); err != nil {
		httpErr(w, 400, "bad request")
		return
	}

	switch q.Op {
	case "up":
		out, err := runCmd(25*time.Second, "tailscale", "up")
		if err != nil {
			httpErr(w, 500, strings.TrimSpace(out+" "+err.Error()))
			return
		}
		writeJSON(w, map[string]any{"ok": true, "output": strings.TrimSpace(out)})

	case "down":
		out, err := runCmd(15*time.Second, "tailscale", "down")
		if err != nil {
			httpErr(w, 500, strings.TrimSpace(out+" "+err.Error()))
			return
		}
		writeJSON(w, map[string]any{"ok": true, "output": strings.TrimSpace(out)})

	case "set":
		args := []string{"set"}
		if q.AdvertiseRoutes != nil {
			clean, err := validateRouteList(*q.AdvertiseRoutes)
			if err != nil {
				httpErr(w, 400, err.Error())
				return
			}
			args = append(args, "--advertise-routes="+clean)
		}
		if q.AdvertiseExitNode != nil {
			args = append(args, fmt.Sprintf("--advertise-exit-node=%v", *q.AdvertiseExitNode))
		}
		if q.AcceptRoutes != nil {
			args = append(args, fmt.Sprintf("--accept-routes=%v", *q.AcceptRoutes))
		}
		if q.AcceptDNS != nil {
			args = append(args, fmt.Sprintf("--accept-dns=%v", *q.AcceptDNS))
		}
		if q.SSH != nil {
			args = append(args, fmt.Sprintf("--ssh=%v", *q.SSH))
		}
		if q.ShieldsUp != nil {
			args = append(args, fmt.Sprintf("--shields-up=%v", *q.ShieldsUp))
		}
		if q.ExitNode != nil {
			en := strings.TrimSpace(*q.ExitNode)
			if en != "" && !reTSHost.MatchString(en) {
				httpErr(w, 400, "invalid exit node")
				return
			}
			args = append(args, "--exit-node="+en)
		}
		if q.ExitNodeAllowLAN != nil {
			args = append(args, fmt.Sprintf("--exit-node-allow-lan-access=%v", *q.ExitNodeAllowLAN))
		}
		if len(args) == 1 {
			httpErr(w, 400, "no options to update")
			return
		}
		out, err := runCmd(20*time.Second, "tailscale", args...)
		if err != nil {
			httpErr(w, 500, strings.TrimSpace(out+" "+err.Error()))
			return
		}
		writeJSON(w, map[string]any{"ok": true, "output": strings.TrimSpace(out)})

	case "ping":
		t := strings.TrimSpace(q.Target)
		if !reTSHost.MatchString(t) {
			httpErr(w, 400, "invalid ping target")
			return
		}
		out, err := runCmd(15*time.Second, "tailscale", "ping", "--c=3", "--timeout=3s", t)
		writeJSON(w, map[string]any{"ok": err == nil, "output": strings.TrimSpace(out)})

	case "netcheck":
		out, err := runCmd(20*time.Second, "tailscale", "netcheck")
		writeJSON(w, map[string]any{"ok": err == nil, "output": strings.TrimSpace(out)})

	case "serve_reset":
		out, err := runCmd(15*time.Second, "tailscale", "serve", "reset")
		if err != nil {
			httpErr(w, 500, strings.TrimSpace(out+" "+err.Error()))
			return
		}
		writeJSON(w, map[string]any{"ok": true, "output": strings.TrimSpace(out)})

	default:
		httpErr(w, 400, "invalid operation")
	}
}
