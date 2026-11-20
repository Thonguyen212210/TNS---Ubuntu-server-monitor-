// TNS — lightweight server monitor & control panel.
package main

import (
	"context"
	"crypto/tls"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

//go:embed web
var webFS embed.FS

const appVersion = "1.2.0"

var (
	flagAddr     = flag.String("addr", ":1611", "listen address")
	flagData     = flag.String("data", "", "data directory (default /var/lib/tns as root, ~/.tns otherwise)")
	flagTLS      = flag.Bool("tls", false, "serve HTTPS with an auto-generated self-signed certificate")
	flagDemo     = flag.Bool("demo", false, "demo mode: synthetic data for subsystems not available on this machine")
	flagNoGeo    = flag.Bool("nogeo", false, "disable online IP geolocation (ip-api.com)")
	flagResetPw   = flag.Bool("reset-password", false, "clear the admin password; it can be set again on next visit")
	flagAgentKey  = flag.String("agent-key", "", "shared cluster secret for hub-to-agent peer requests")
	flagAllowHost = flag.String("allow-host", "", "only allow requests matching this domain (blocks direct IP access)")
	flagAutocert  = flag.Bool("autocert", false, "obtain and auto-renew a Let's Encrypt TLS certificate on :443/:80 for -allow-host")
)

var dataDir string

func main() {
	flag.Parse()
	dataDir = *flagData
	if dataDir == "" {
		if os.Geteuid() == 0 {
			dataDir = "/var/lib/tns"
		} else {
			home, _ := os.UserHomeDir()
			dataDir = filepath.Join(home, ".tns")
		}
		// one-time migration from the old "orbit" name
		old := filepath.Join(filepath.Dir(dataDir), strings.Replace(filepath.Base(dataDir), "tns", "orbit", 1))
		if _, err := os.Stat(dataDir); os.IsNotExist(err) {
			if _, err := os.Stat(old); err == nil {
				if os.Rename(old, dataDir) == nil {
					log.Printf("migrated data dir %s -> %s", old, dataDir)
				}
			}
		}
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		log.Fatalf("cannot create data dir: %v", err)
	}
	loadStore()
	if *flagResetPw {
		store.reset()
		fmt.Println("Password cleared. Open the panel to set a new one.")
		return
	}

	startCollector()
	startLogins()
	loadNodes()
	initAzure()
	if *flagDemo {
		initDemo()
	}

	mux := http.NewServeMux()
	sub, _ := fs.Sub(webFS, "web")
	static := http.FileServer(http.FS(sub))
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/vendor/") {
			w.Header().Set("Cache-Control", "public, max-age=86400")
		} else {
			w.Header().Set("Cache-Control", "no-store")
		}
		static.ServeHTTP(w, r)
	})

	// auth
	mux.HandleFunc("GET /api/auth/state", handleAuthState)
	mux.HandleFunc("POST /api/auth/setup", handleSetup)
	mux.HandleFunc("POST /api/auth/login", handleLogin)
	mux.HandleFunc("POST /api/auth/logout", handleLogout)
	mux.HandleFunc("POST /api/auth/password", auth(handleChangePassword))
	mux.HandleFunc("GET /api/auth/passkey", auth(handlePasskeyList))
	mux.HandleFunc("POST /api/auth/passkey/register/begin", auth(handlePasskeyRegBegin))
	mux.HandleFunc("POST /api/auth/passkey/register/finish", auth(handlePasskeyRegFinish))
	mux.HandleFunc("POST /api/auth/passkey/delete", auth(handlePasskeyDelete))
	mux.HandleFunc("POST /api/auth/passkey/mode", auth(handlePasskeyMode))
	mux.HandleFunc("POST /api/auth/passkey/login/begin", handlePasskeyLoginBegin)
	mux.HandleFunc("POST /api/auth/passkey/login/finish", handlePasskeyLoginFinish)

	// nodes & Azure infrastructure (always handled by hub)
	mux.HandleFunc("GET /api/nodes", auth(handleNodesList))
	mux.HandleFunc("POST /api/nodes", auth(handleNodeUpsert))
	mux.HandleFunc("POST /api/nodes/delete", auth(handleNodeDelete))
	mux.HandleFunc("GET /api/azure/overview", auth(handleAzureOverview))
	mux.HandleFunc("POST /api/azure/nsg/rule", auth(handleAzureNSGRule))
	mux.HandleFunc("POST /api/azure/route", auth(handleAzureRoute))
	mux.HandleFunc("POST /api/azure/subnet", auth(handleAzureSubnet))
	mux.HandleFunc("POST /api/azure/nic/forwarding", auth(handleAzureNICForwarding))
	mux.HandleFunc("POST /api/azure/vm/action", auth(handleAzureVMAction))
	mux.HandleFunc("POST /api/azure/gateway", auth(handleAzureGateway))

	// system (node-aware)
	mux.HandleFunc("GET /api/sys/info", auth(nodeProxy(handleSysInfo)))
	mux.HandleFunc("GET /api/sys/stats", auth(nodeProxy(handleSysStats)))
	mux.HandleFunc("GET /api/sys/processes", auth(nodeProxy(handleProcesses)))
	mux.HandleFunc("POST /api/sys/kill", auth(nodeProxy(handleKill)))
	mux.HandleFunc("GET /api/sys/ports", auth(nodeProxy(handlePorts)))

	// security (node-aware)
	mux.HandleFunc("GET /api/logins", auth(nodeProxy(handleLogins)))
	mux.HandleFunc("GET /api/firewall", auth(nodeProxy(handleFirewall)))
	mux.HandleFunc("POST /api/firewall", auth(nodeProxy(handleFirewallAction)))
	mux.HandleFunc("GET /api/tailscale/status", auth(nodeProxy(handleTailscaleStatus)))
	mux.HandleFunc("POST /api/tailscale/action", auth(nodeProxy(handleTailscaleAction)))

	// apps (node-aware)
	mux.HandleFunc("GET /api/docker", auth(nodeProxy(handleDockerList)))
	mux.HandleFunc("POST /api/docker/action", auth(nodeProxy(handleDockerAction)))
	mux.HandleFunc("GET /api/docker/logs", auth(nodeProxy(handleDockerLogs)))
	mux.HandleFunc("GET /api/services", auth(nodeProxy(handleServices)))
	mux.HandleFunc("POST /api/services/action", auth(nodeProxy(handleServiceAction)))
	mux.HandleFunc("GET /api/services/logs", auth(nodeProxy(handleServiceLogs)))
	mux.HandleFunc("GET /api/sites", auth(nodeProxy(handleSites)))
	mux.HandleFunc("GET /api/sites/file", auth(nodeProxy(handleSiteFile)))
	mux.HandleFunc("POST /api/sites/action", auth(nodeProxy(handleSiteAction)))

	// files (node-aware)
	mux.HandleFunc("GET /api/files/list", auth(nodeProxy(handleFilesList)))
	mux.HandleFunc("GET /api/files/raw", auth(nodeProxy(handleFilesRaw)))
	mux.HandleFunc("GET /api/files/text", auth(nodeProxy(handleFilesText)))
	mux.HandleFunc("POST /api/files/save", auth(nodeProxy(handleFilesSave)))
	mux.HandleFunc("POST /api/files/upload", auth(nodeProxy(handleFilesUpload)))
	mux.HandleFunc("POST /api/files/op", auth(nodeProxy(handleFilesOp)))

	// terminal (node-aware via proxyTerminalWS)
	mux.HandleFunc("GET /ws/term", auth(handleTerminal))

	allowHost := strings.ToLower(strings.TrimSpace(*flagAllowHost))
	handler := hostGuard(allowHost, securityHeaders(mux))

	srv := &http.Server{
		Addr:              *flagAddr,
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
	}
	scheme := "http"
	if *flagTLS || *flagAutocert {
		scheme = "https"
	}
	log.Printf("TNS %s listening on %s://%s  (data: %s, demo: %v, allowHost: %q, autocert: %v)",
		appVersion, scheme, displayAddr(*flagAddr), dataDir, *flagDemo, allowHost, *flagAutocert)
	if !store.hasPassword() {
		tok := ensureSetupToken()
		log.Printf("First run: open the panel in a browser to set the admin password.")
		log.Printf("Setup token (required when not connecting from localhost): %s  [stored in %s]", tok, setupTokenPath())
	}
	var err error
	if *flagAutocert {
		if allowHost == "" {
			log.Fatal("-autocert requires -allow-host <domain>")
		}
		certDir := filepath.Join(dataDir, "autocert")
		_ = os.MkdirAll(certDir, 0o700)
		m := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			Cache:      autocert.DirCache(certDir),
			HostPolicy: autocert.HostWhitelist(allowHost),
		}
		// HTTP :80 serves ACME HTTP-01 challenges and redirects ONLY the allowed domain to HTTPS;
		// direct IP requests on :80 are rejected with 403.
		httpSrv := &http.Server{
			Addr:              ":80",
			ReadHeaderTimeout: 10 * time.Second,
			Handler: m.HTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !hostAllowed(allowHost, r) {
					http.Error(w, "Direct IP access is disabled. Please use "+allowHost, http.StatusForbidden)
					return
				}
				target := "https://" + allowHost + r.URL.RequestURI()
				http.Redirect(w, r, target, http.StatusMovedPermanently)
			})),
		}
		go func() {
			log.Printf("ACME HTTP-01 & HTTPS redirect server listening on :80 (domain-only: %s)", allowHost)
			if herr := httpSrv.ListenAndServe(); herr != nil {
				log.Printf("http :80 server error: %v", herr)
			}
		}()
		tlsCfg := m.TLSConfig()
		tlsCfg.MinVersion = tls.VersionTLS12
		origGetCert := tlsCfg.GetCertificate
		tlsCfg.GetCertificate = func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			if !strings.EqualFold(strings.TrimSpace(hello.ServerName), allowHost) {
				return nil, fmt.Errorf("tls: direct IP / unknown SNI %q blocked", hello.ServerName)
			}
			return origGetCert(hello)
		}
		srv.TLSConfig = tlsCfg
		err = srv.ListenAndServeTLS("", "")
	} else if *flagTLS {
		cert, key, cerr := ensureSelfSignedCert(dataDir)
		if cerr != nil {
			log.Fatalf("tls: %v", cerr)
		}
		if allowHost != "" {
			loaded, lerr := tls.LoadX509KeyPair(cert, key)
			if lerr != nil {
				log.Fatalf("tls load: %v", lerr)
			}
			srv.TLSConfig = &tls.Config{
				MinVersion: tls.VersionTLS12,
				GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
					if hello.ServerName != "" && !strings.EqualFold(hello.ServerName, allowHost) {
						return nil, fmt.Errorf("tls: host %q blocked", hello.ServerName)
					}
					if hello.ServerName == "" {
						return nil, fmt.Errorf("tls: direct IP access blocked")
					}
					return &loaded, nil
				},
			}
			err = srv.ListenAndServeTLS("", "")
		} else {
			err = srv.ListenAndServeTLS(cert, key)
		}
	} else {
		err = srv.ListenAndServe()
	}
	log.Fatal(err)
}

func requestHostOnly(hostport string) string {
	hostport = strings.TrimSpace(hostport)
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return strings.ToLower(strings.Trim(h, "[]"))
	}
	return strings.ToLower(strings.Trim(hostport, "[]"))
}

func hostAllowed(allowHost string, r *http.Request) bool {
	if allowHost == "" {
		return true
	}
	if isLoopback(r) || validPeerKey(r) {
		return true
	}
	h := requestHostOnly(r.Host)
	if h == "" || net.ParseIP(h) != nil {
		return false
	}
	return h == allowHost
}

func hostGuard(allowHost string, next http.Handler) http.Handler {
	if allowHost == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(allowHost, r) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("Direct IP access is disabled.\n"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func displayAddr(a string) string {
	if strings.HasPrefix(a, ":") {
		return "0.0.0.0" + a
	}
	return a
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if !strings.HasPrefix(r.URL.Path, "/api/files/raw") {
			w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		}
		h.ServeHTTP(w, r)
	})
}

// ---------- helpers ----------

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 16<<20))
	return dec.Decode(v)
}

// runCmd executes a program (never through a shell) with a timeout and returns combined output.
func runCmd(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

func hasCmd(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func isRoot() bool { return os.Geteuid() == 0 }
