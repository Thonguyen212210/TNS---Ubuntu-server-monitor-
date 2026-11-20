package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/subtle"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

const sessionCookie = "tns_session"
const sessionTTL = 12 * time.Hour

type Store struct {
	mu           sync.Mutex
	PasswordHash string    `json:"password_hash"`
	SetAt        time.Time `json:"set_at"`
	UserHandle   string    `json:"user_handle,omitempty"`
	Passkeys     []Passkey `json:"passkeys,omitempty"`
	PasskeyOnly  bool      `json:"passkey_only,omitempty"`
}

var store = &Store{}

func storePath() string { return filepath.Join(dataDir, "config.json") }

func loadStore() {
	b, err := os.ReadFile(storePath())
	if err == nil {
		_ = json.Unmarshal(b, store)
	}
}

func (s *Store) save() error {
	b, _ := json.MarshalIndent(s, "", "  ")
	tmp := storePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, storePath())
}

func (s *Store) hasPassword() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.PasswordHash != ""
}

func (s *Store) setPassword(pw string) error {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), 12)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.PasswordHash = string(h)
	s.SetAt = time.Now()
	return s.save()
}

func (s *Store) check(pw string) bool {
	s.mu.Lock()
	h := s.PasswordHash
	s.mu.Unlock()
	return h != "" && bcrypt.CompareHashAndPassword([]byte(h), []byte(pw)) == nil
}

func (s *Store) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.PasswordHash = ""
	s.PasskeyOnly = false
	_ = s.save()
}

// ---------- sessions ----------

var (
	sessMu   sync.Mutex
	sessions = map[string]time.Time{}
)

func newSession() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	tok := hex.EncodeToString(b)
	sessMu.Lock()
	sessions[tok] = time.Now().Add(sessionTTL)
	sessMu.Unlock()
	return tok
}

func validSession(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return false
	}
	sessMu.Lock()
	defer sessMu.Unlock()
	exp, ok := sessions[c.Value]
	if !ok || time.Now().After(exp) {
		delete(sessions, c.Value)
		return false
	}
	sessions[c.Value] = time.Now().Add(sessionTTL) // sliding expiry
	return true
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, tok string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil, MaxAge: maxAge,
	})
}

// auth wraps a handler: requires a valid session (or peer cluster key); state-changing requests must
// also carry the X-TNS header (blocks cross-site form posts).
func auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if validPeerKey(r) {
			h(w, r)
			return
		}
		if !validSession(r) {
			httpErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if r.Method != http.MethodGet && r.Header.Get("X-TNS") != "1" {
			httpErr(w, http.StatusForbidden, "missing X-TNS header")
			return
		}
		h(w, r)
	}
}

// ---------- brute-force protection for the panel login ----------

type failRec struct {
	count int
	until time.Time
}

var (
	failMu sync.Mutex
	fails  = map[string]*failRec{}
)

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func lockedOut(ip string) time.Duration {
	failMu.Lock()
	defer failMu.Unlock()
	if f, ok := fails[ip]; ok && time.Now().Before(f.until) {
		return time.Until(f.until)
	}
	return 0
}

func recordFail(ip string) {
	failMu.Lock()
	defer failMu.Unlock()
	f := fails[ip]
	if f == nil {
		f = &failRec{}
		fails[ip] = f
	}
	f.count++
	if f.count >= 5 {
		f.until = time.Now().Add(time.Duration(f.count-4) * time.Minute)
	}
}

func clearFail(ip string) {
	failMu.Lock()
	delete(fails, ip)
	failMu.Unlock()
}

// ---------- handlers ----------

func handleAuthState(w http.ResponseWriter, r *http.Request) {
	host, _ := os.Hostname()
	writeJSON(w, map[string]any{
		"setup":       !store.hasPassword(),
		"needToken":   !store.hasPassword() && !isLoopback(r),
		"authed":      validSession(r),
		"hasPasskey":  store.hasPasskeys(),
		"passkeyOnly": store.isPasskeyOnly(),
		"hostname":    host,
		"version":     appVersion,
		"demo":        *flagDemo,
	})
}

func validPassword(pw string) string {
	if n := utf8.RuneCountInString(pw); n < 8 {
		return fmt.Sprintf("Password must be at least 8 characters (received %d)", n)
	}
	if len(pw) > 72 {
		return "Password must be at most 72 bytes"
	}
	return ""
}

func handleSetup(w http.ResponseWriter, r *http.Request) {
	if store.hasPassword() {
		httpErr(w, http.StatusConflict, "already initialized")
		return
	}
	var req struct{ Password, Token string }
	if err := readJSON(r, &req); err != nil {
		httpErr(w, 400, "bad request")
		return
	}
	ip := clientIP(r)
	if !isLoopback(r) {
		if d := lockedOut(ip); d > 0 {
			httpErr(w, http.StatusTooManyRequests, "Too many attempts. Try again in "+d.Round(time.Second).String())
			return
		}
		if tok := setupToken(); tok == "" || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(req.Token)), []byte(tok)) != 1 {
			recordFail(ip)
			log.Printf("setup rejected from %s: bad setup token", ip)
			httpErr(w, 403, "Invalid setup token. View on server: sudo cat "+setupTokenPath())
			return
		}
	}
	if msg := validPassword(req.Password); msg != "" {
		log.Printf("setup rejected from %s: %s", clientIP(r), msg)
		httpErr(w, 400, msg)
		return
	}
	if err := store.setPassword(req.Password); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	_ = os.Remove(setupTokenPath())
	log.Printf("admin password set from %s", ip)
	setSessionCookie(w, r, newSession(), int(sessionTTL.Seconds()))
	writeJSON(w, map[string]bool{"ok": true})
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	if store.isPasskeyOnly() {
		httpErr(w, http.StatusForbidden, "Password login is disabled — please sign in with a Passkey")
		return
	}
	ip := clientIP(r)
	if d := lockedOut(ip); d > 0 {
		httpErr(w, http.StatusTooManyRequests, "Too many attempts. Try again in "+d.Round(time.Second).String())
		return
	}
	var req struct{ Password string }
	if err := readJSON(r, &req); err != nil {
		httpErr(w, 400, "bad request")
		return
	}
	if !store.check(req.Password) {
		recordFail(ip)
		recordPanelLogin(ip, false)
		time.Sleep(400 * time.Millisecond)
		httpErr(w, http.StatusUnauthorized, "Incorrect password")
		return
	}
	clearFail(ip)
	recordPanelLogin(ip, true)
	setSessionCookie(w, r, newSession(), int(sessionTTL.Seconds()))
	writeJSON(w, map[string]bool{"ok": true})
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		sessMu.Lock()
		delete(sessions, c.Value)
		sessMu.Unlock()
	}
	setSessionCookie(w, r, "", -1)
	writeJSON(w, map[string]bool{"ok": true})
}

func handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req struct{ Current, New string }
	if err := readJSON(r, &req); err != nil {
		httpErr(w, 400, "bad request")
		return
	}
	if !store.check(req.Current) {
		httpErr(w, 403, "Current password is incorrect")
		return
	}
	if msg := validPassword(req.New); msg != "" {
		httpErr(w, 400, msg)
		return
	}
	if err := store.setPassword(req.New); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	// invalidate all other sessions
	sessMu.Lock()
	sessions = map[string]time.Time{}
	sessMu.Unlock()
	setSessionCookie(w, r, newSession(), int(sessionTTL.Seconds()))
	writeJSON(w, map[string]bool{"ok": true})
}

// ---------- TLS ----------

func ensureSelfSignedCert(dir string) (string, string, error) {
	certPath, keyPath := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	if _, err := os.Stat(certPath); err == nil {
		return certPath, keyPath, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	host, _ := os.Hostname()
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host, Organization: []string{"Azure TNS Console"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(5, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{host, "localhost"},
	}
	ifaces, _ := net.InterfaceAddrs()
	for _, a := range ifaces {
		if ipn, ok := a.(*net.IPNet); ok {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ipn.IP)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return "", "", err
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		return "", "", err
	}
	return certPath, keyPath, nil
}

// ---------- one-time setup token ----------
// Until a password exists, remote clients must present a token that is only
// readable on the server, so nobody else can claim a freshly installed panel.

func setupTokenPath() string { return filepath.Join(dataDir, "setup-token") }

func setupToken() string {
	b, err := os.ReadFile(setupTokenPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func ensureSetupToken() string {
	if t := setupToken(); t != "" {
		return t
	}
	b := make([]byte, 9)
	_, _ = rand.Read(b)
	t := strings.ToUpper(hex.EncodeToString(b))
	t = t[0:6] + "-" + t[6:12] + "-" + t[12:18]
	_ = os.WriteFile(setupTokenPath(), []byte(t+"\n"), 0o600)
	return t
}

func isLoopback(r *http.Request) bool {
	ip := net.ParseIP(clientIP(r))
	return ip != nil && ip.IsLoopback()
}
