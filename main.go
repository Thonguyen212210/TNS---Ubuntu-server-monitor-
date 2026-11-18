// TNS — lightweight server monitor & control panel.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

//go:embed web
var webFS embed.FS

const appVersion = "1.0.0"

var (
	flagAddr    = flag.String("addr", ":1611", "listen address")
	flagData    = flag.String("data", "", "data directory (default /var/lib/tns as root, ~/.tns otherwise)")
	flagTLS     = flag.Bool("tls", false, "serve HTTPS with an auto-generated self-signed certificate")
	flagDemo    = flag.Bool("demo", false, "demo mode: synthetic data for subsystems not available on this machine")
	flagNoGeo   = flag.Bool("nogeo", false, "disable online IP geolocation (ip-api.com)")
	flagResetPw = flag.Bool("reset-password", false, "clear the admin password; it can be set again on next visit")
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
			w.Header().Set("Cache-Control", "no-cache")
		}
		static.ServeHTTP(w, r)
	})

	// auth
	mux.HandleFunc("GET /api/auth/state", handleAuthState)
	mux.HandleFunc("POST /api/auth/setup", handleSetup)
	mux.HandleFunc("POST /api/auth/login", handleLogin)
	mux.HandleFunc("POST /api/auth/logout", handleLogout)
	mux.HandleFunc("POST /api/auth/password", auth(handleChangePassword))

	// system
	mux.HandleFunc("GET /api/sys/info", auth(handleSysInfo))
	mux.HandleFunc("GET /api/sys/stats", auth(handleSysStats))
	mux.HandleFunc("GET /api/sys/processes", auth(handleProcesses))
	mux.HandleFunc("POST /api/sys/kill", auth(handleKill))
	mux.HandleFunc("GET /api/sys/ports", auth(handlePorts))

	// security
	mux.HandleFunc("GET /api/logins", auth(handleLogins))
	mux.HandleFunc("GET /api/firewall", auth(handleFirewall))
	mux.HandleFunc("POST /api/firewall", auth(handleFirewallAction))

	// apps
	mux.HandleFunc("GET /api/docker", auth(handleDockerList))
	mux.HandleFunc("POST /api/docker/action", auth(handleDockerAction))
	mux.HandleFunc("GET /api/docker/logs", auth(handleDockerLogs))
	mux.HandleFunc("GET /api/services", auth(handleServices))
	mux.HandleFunc("POST /api/services/action", auth(handleServiceAction))
	mux.HandleFunc("GET /api/services/logs", auth(handleServiceLogs))
	mux.HandleFunc("GET /api/sites", auth(handleSites))
	mux.HandleFunc("GET /api/sites/file", auth(handleSiteFile))
	mux.HandleFunc("POST /api/sites/action", auth(handleSiteAction))

	// files
	mux.HandleFunc("GET /api/files/list", auth(handleFilesList))
	mux.HandleFunc("GET /api/files/raw", auth(handleFilesRaw))
	mux.HandleFunc("GET /api/files/text", auth(handleFilesText))
	mux.HandleFunc("POST /api/files/save", auth(handleFilesSave))
	mux.HandleFunc("POST /api/files/upload", auth(handleFilesUpload))
	mux.HandleFunc("POST /api/files/op", auth(handleFilesOp))

	// terminal
	mux.HandleFunc("GET /ws/term", auth(handleTerminal))

	srv := &http.Server{
		Addr:              *flagAddr,
		Handler:           securityHeaders(mux),
		ReadHeaderTimeout: 15 * time.Second,
	}
	scheme := "http"
	if *flagTLS {
		scheme = "https"
	}
	log.Printf("TNS %s listening on %s://%s  (data: %s, demo: %v)", appVersion, scheme, displayAddr(*flagAddr), dataDir, *flagDemo)
	if !store.hasPassword() {
		log.Printf("First run: open the panel in a browser to set the admin password.")
	}
	var err error
	if *flagTLS {
		cert, key, cerr := ensureSelfSignedCert(dataDir)
		if cerr != nil {
			log.Fatalf("tls: %v", cerr)
		}
		err = srv.ListenAndServeTLS(cert, key)
	} else {
		err = srv.ListenAndServe()
	}
	log.Fatal(err)
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
