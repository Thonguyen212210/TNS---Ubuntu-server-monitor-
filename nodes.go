package main

import (
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shirou/gopsutil/v4/host"
	gnet "github.com/shirou/gopsutil/v4/net"
)

type NodeConfig struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Endpoint    string `json:"endpoint"`
	PeerKey     string `json:"peerKey,omitempty"`
	PrivateIP   string `json:"privateIp,omitempty"`
	Subnet      string `json:"subnet,omitempty"`
	Role        string `json:"role,omitempty"`
	AzureVMName string `json:"azureVmName,omitempty"`
}

type NodeStatus struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Local       bool           `json:"local"`
	Online      bool           `json:"online"`
	Endpoint    string         `json:"endpoint,omitempty"`
	PrivateIP   string         `json:"privateIp,omitempty"`
	PublicIP    string         `json:"publicIp,omitempty"`
	TailscaleIP string         `json:"tailscaleIp,omitempty"`
	Subnet      string         `json:"subnet,omitempty"`
	Role        string         `json:"role,omitempty"`
	AzureVMName string         `json:"azureVmName,omitempty"`
	Arch        string         `json:"arch,omitempty"`
	OS          string         `json:"os,omitempty"`
	LatencyMs   int64          `json:"latencyMs,omitempty"`
	Error       string         `json:"error,omitempty"`
	Stats       *Snapshot      `json:"stats,omitempty"`
	Info        map[string]any `json:"info,omitempty"`
}

var (
	nodesMu      sync.RWMutex
	nodesList    []NodeConfig
	agentKeyVal  string
	reNodeID     = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)
	peerHTTPTr   = &http.Transport{
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     60 * time.Second,
	}
	peerProxyClient = &http.Client{Transport: peerHTTPTr}
	peerProbeClient = &http.Client{Transport: peerHTTPTr, Timeout: 3 * time.Second}
)

func nodesPath() string    { return filepath.Join(dataDir, "nodes.json") }
func agentKeyPath() string { return filepath.Join(dataDir, "agent-key") }

func loadNodes() {
	if *flagAgentKey != "" {
		agentKeyVal = strings.TrimSpace(*flagAgentKey)
	} else if b, err := os.ReadFile(agentKeyPath()); err == nil {
		agentKeyVal = strings.TrimSpace(string(b))
	}
	b, err := os.ReadFile(nodesPath())
	if err != nil {
		return
	}
	var list []NodeConfig
	if err := json.Unmarshal(b, &list); err != nil {
		log.Printf("nodes: parse %s: %v", nodesPath(), err)
		return
	}
	nodesMu.Lock()
	nodesList = list
	nodesMu.Unlock()
}

func saveNodesLocked() error {
	b, err := json.MarshalIndent(nodesList, "", "  ")
	if err != nil {
		return err
	}
	tmp := nodesPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, nodesPath())
}

func validPeerKey(r *http.Request) bool {
	if agentKeyVal == "" {
		return false
	}
	got := r.Header.Get("X-TNS-Peer-Key")
	if got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(agentKeyVal)) == 1
}

func getPeerNode(id string) (NodeConfig, bool) {
	nodesMu.RLock()
	defer nodesMu.RUnlock()
	for _, n := range nodesList {
		if n.ID == id {
			return n, true
		}
	}
	return NodeConfig{}, false
}

func getAllPeers() []NodeConfig {
	nodesMu.RLock()
	defer nodesMu.RUnlock()
	out := make([]NodeConfig, len(nodesList))
	copy(out, nodesList)
	return out
}

func targetNodeID(r *http.Request) string {
	if id := strings.TrimSpace(r.Header.Get("X-TNS-Node")); id != "" {
		return id
	}
	return strings.TrimSpace(r.URL.Query().Get("node"))
}

// nodeProxy transparently proxies node-scoped HTTP endpoints to a managed peer node
// when X-TNS-Node header or ?node=<id> query param selects a remote VM.
func nodeProxy(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := targetNodeID(r)
		if id == "" || id == "local" {
			h(w, r)
			return
		}
		peer, ok := getPeerNode(id)
		if !ok {
			httpErr(w, http.StatusNotFound, "server not found: "+id)
			return
		}
		proxyHTTPToPeer(w, r, peer)
	}
}

func buildPeerURL(endpoint, reqPath string, rawQuery string, ws bool) (string, error) {
	u, err := url.Parse(strings.TrimRight(endpoint, "/"))
	if err != nil {
		return "", err
	}
	if ws {
		if u.Scheme == "https" {
			u.Scheme = "wss"
		} else {
			u.Scheme = "ws"
		}
	}
	u.Path = reqPath
	q, _ := url.ParseQuery(rawQuery)
	q.Del("node")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

var hopHeaders = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true,
	"Proxy-Authorization": true, "Te": true, "Trailers": true,
	"Transfer-Encoding": true, "Upgrade": true,
}

func proxyHTTPToPeer(w http.ResponseWriter, r *http.Request, peer NodeConfig) {
	target, err := buildPeerURL(peer.Endpoint, r.URL.Path, r.URL.RawQuery, false)
	if err != nil {
		httpErr(w, 502, "invalid peer endpoint: "+err.Error())
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		httpErr(w, 502, err.Error())
		return
	}
	for k, vals := range r.Header {
		if hopHeaders[k] || strings.EqualFold(k, "Cookie") || strings.EqualFold(k, "Host") ||
			strings.EqualFold(k, "Origin") || strings.EqualFold(k, "X-TNS-Node") {
			continue
		}
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("X-TNS", "1")
	if peer.PeerKey != "" {
		req.Header.Set("X-TNS-Peer-Key", peer.PeerKey)
	}
	req.ContentLength = r.ContentLength

	resp, err := peerProxyClient.Do(req)
	if err != nil {
		httpErr(w, 502, fmt.Sprintf("unable to reach %s (%s): %v", peer.Name, peer.Endpoint, err))
		return
	}
	defer resp.Body.Close()

	for k, vals := range resp.Header {
		if hopHeaders[k] || strings.EqualFold(k, "Set-Cookie") {
			continue
		}
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func proxyTerminalWS(w http.ResponseWriter, r *http.Request, peer NodeConfig) {
	target, err := buildPeerURL(peer.Endpoint, "/ws/term", r.URL.RawQuery, true)
	if err != nil {
		httpErr(w, 502, "invalid peer endpoint")
		return
	}
	clientConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer clientConn.Close()

	hdr := http.Header{}
	if peer.PeerKey != "" {
		hdr.Set("X-TNS-Peer-Key", peer.PeerKey)
	}
	dialer := websocket.Dialer{
		TLSClientConfig:  &tls.Config{InsecureSkipVerify: true},
		HandshakeTimeout: 10 * time.Second,
	}
	peerConn, resp, err := dialer.Dial(target, hdr)
	if err != nil {
		msg := fmt.Sprintf("\r\n\x1b[31mUnable to open terminal to %s (%s): %v\x1b[0m\r\n", peer.Name, peer.PrivateIP, err)
		if resp != nil {
			msg = fmt.Sprintf("\r\n\x1b[31mPeer %s returned HTTP %d\x1b[0m\r\n", peer.Name, resp.StatusCode)
		}
		_ = clientConn.WriteMessage(websocket.BinaryMessage, []byte(msg))
		return
	}
	defer peerConn.Close()

	done := make(chan struct{}, 2)
	pump := func(dst, src *websocket.Conn) {
		defer func() { done <- struct{}{} }()
		for {
			mt, data, err := src.ReadMessage()
			if err != nil {
				return
			}
			if err := dst.WriteMessage(mt, data); err != nil {
				return
			}
		}
	}
	go pump(peerConn, clientConn)
	go pump(clientConn, peerConn)
	<-done
}

func localPrivateAndTailscaleIP() (priv, ts string) {
	ifs, err := gnet.Interfaces()
	if err != nil {
		return "", ""
	}
	for _, i := range ifs {
		if i.Name == "lo" || strings.HasPrefix(i.Name, "lo0") || strings.HasPrefix(i.Name, "docker") || strings.HasPrefix(i.Name, "br-") || strings.HasPrefix(i.Name, "veth") {
			continue
		}
		for _, a := range i.Addrs {
			ipStr := a.Addr
			if idx := strings.IndexByte(ipStr, '/'); idx >= 0 {
				ipStr = ipStr[:idx]
			}
			ip := net.ParseIP(ipStr)
			if ip == nil || ip.To4() == nil {
				continue
			}
			if strings.HasPrefix(i.Name, "tailscale") || strings.HasPrefix(ipStr, "100.") {
				if ts == "" {
					ts = ipStr
				}
			} else if priv == "" {
				priv = ipStr
			}
		}
	}
	return priv, ts
}

func probePeer(peer NodeConfig) NodeStatus {
	st := NodeStatus{
		ID:          peer.ID,
		Name:        peer.Name,
		Endpoint:    peer.Endpoint,
		PrivateIP:   peer.PrivateIP,
		Subnet:      peer.Subnet,
		Role:        peer.Role,
		AzureVMName: peer.AzureVMName,
	}
	if st.Name == "" {
		st.Name = peer.ID
	}
	t0 := time.Now()
	doGet := func(path string) ([]byte, error) {
		u, err := buildPeerURL(peer.Endpoint, path, "", false)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequest("GET", u, nil)
		if err != nil {
			return nil, err
		}
		if peer.PeerKey != "" {
			req.Header.Set("X-TNS-Peer-Key", peer.PeerKey)
		}
		resp, err := peerProbeClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
		}
		return b, err
	}

	bStats, err := doGet("/api/sys/stats")
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.LatencyMs = time.Since(t0).Milliseconds()
	st.Online = true

	var sWrap struct {
		Current Snapshot `json:"current"`
	}
	if json.Unmarshal(bStats, &sWrap) == nil {
		snap := sWrap.Current
		st.Stats = &snap
	}
	if bInfo, err := doGet("/api/sys/info"); err == nil {
		var info map[string]any
		if json.Unmarshal(bInfo, &info) == nil {
			st.Info = info
			if a, ok := info["arch"].(string); ok {
				st.Arch = a
			}
			plat, _ := info["platform"].(string)
			ver, _ := info["platformVersion"].(string)
			if plat != "" {
				st.OS = strings.TrimSpace(plat + " " + ver)
			} else if o, ok := info["os"].(string); ok {
				st.OS = o
			}
		}
	}
	return st
}

func handleNodesList(w http.ResponseWriter, r *http.Request) {
	hostname, _ := os.Hostname()
	priv, ts := localPrivateAndTailscaleIP()
	metMu.RLock()
	snap := current
	metMu.RUnlock()

	osLabel := ""
	archLabel := ""
	if hi, err := host.Info(); err == nil && hi != nil {
		osLabel = strings.TrimSpace(hi.Platform + " " + hi.PlatformVersion)
		archLabel = hi.KernelArch
	}
	localNode := NodeStatus{
		ID:          "local",
		Name:        hostname,
		Local:       true,
		Online:      true,
		PrivateIP:   priv,
		TailscaleIP: ts,
		Role:        "Hub / Gateway",
		AzureVMName: hostname,
		Arch:        archLabel,
		OS:          osLabel,
		Stats:       &snap,
	}
	if md := getIMDSCached(); md != nil {
		if md.Compute.Name != "" {
			localNode.AzureVMName = md.Compute.Name
		}
		if len(md.Network.Interface) > 0 {
			ni := md.Network.Interface[0]
			if len(ni.IPv4.IPAddress) > 0 {
				if ni.IPv4.IPAddress[0].PrivateIPAddress != "" {
					localNode.PrivateIP = ni.IPv4.IPAddress[0].PrivateIPAddress
				}
				if ni.IPv4.IPAddress[0].PublicIPAddress != "" {
					localNode.PublicIP = ni.IPv4.IPAddress[0].PublicIPAddress
				}
			}
			if len(ni.IPv4.Subnet) > 0 {
				localNode.Subnet = ni.IPv4.Subnet[0].Address + "/" + ni.IPv4.Subnet[0].Prefix
			}
		}
	}

	peers := getAllPeers()
	results := make([]NodeStatus, len(peers))
	var wg sync.WaitGroup
	for i, p := range peers {
		wg.Add(1)
		go func(idx int, cfg NodeConfig) {
			defer wg.Done()
			results[idx] = probePeer(cfg)
		}(i, p)
	}
	wg.Wait()

	all := append([]NodeStatus{localNode}, results...)
	writeJSON(w, map[string]any{"nodes": all})
}

func handleNodeUpsert(w http.ResponseWriter, r *http.Request) {
	var q NodeConfig
	if err := readJSON(r, &q); err != nil {
		httpErr(w, 400, "bad request")
		return
	}
	q.ID = strings.TrimSpace(q.ID)
	q.Name = strings.TrimSpace(q.Name)
	q.Endpoint = strings.TrimRight(strings.TrimSpace(q.Endpoint), "/")
	if !reNodeID.MatchString(q.ID) || q.ID == "local" {
		httpErr(w, 400, "Invalid server ID (alphanumeric, -, _, . only, cannot be 'local')")
		return
	}
	if q.Name == "" {
		q.Name = q.ID
	}
	u, err := url.Parse(q.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		httpErr(w, 400, "Endpoint must be a valid http:// or https:// URL")
		return
	}
	if q.PrivateIP == "" {
		q.PrivateIP = u.Hostname()
	}
	if q.AzureVMName == "" {
		q.AzureVMName = q.Name
	}

	nodesMu.Lock()
	replaced := false
	for i := range nodesList {
		if nodesList[i].ID == q.ID {
			if q.PeerKey == "" {
				q.PeerKey = nodesList[i].PeerKey
			}
			nodesList[i] = q
			replaced = true
			break
		}
	}
	if !replaced {
		nodesList = append(nodesList, q)
	}
	err = saveNodesLocked()
	nodesMu.Unlock()
	if err != nil {
		httpErr(w, 500, "failed to save nodes.json: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "status": probePeer(q)})
}

func handleNodeDelete(w http.ResponseWriter, r *http.Request) {
	var q struct {
		ID string `json:"id"`
	}
	if err := readJSON(r, &q); err != nil || q.ID == "" || q.ID == "local" {
		httpErr(w, 400, "invalid node ID")
		return
	}
	nodesMu.Lock()
	out := nodesList[:0]
	found := false
	for _, n := range nodesList {
		if n.ID == q.ID {
			found = true
			continue
		}
		out = append(out, n)
	}
	nodesList = out
	err := saveNodesLocked()
	nodesMu.Unlock()
	if !found {
		httpErr(w, 404, "node not found")
		return
	}
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
