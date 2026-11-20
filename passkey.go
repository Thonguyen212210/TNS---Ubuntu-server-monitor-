package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Pure-Go W3C WebAuthn (Passkey) implementation supporting ES256 (-7), RS256 (-257), and EdDSA (-8).

type Passkey struct {
	ID         string    `json:"id"` // base64url (unpadded) credential ID
	Name       string    `json:"name"`
	RPID       string    `json:"rpId"`
	Alg        int       `json:"alg"`       // -7 (ES256), -257 (RS256), -8 (EdDSA)
	PublicKey  string    `json:"publicKey"` // base64 std PKIX SPKI DER
	SignCount  uint32    `json:"signCount"`
	Transports []string  `json:"transports,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	LastUsedAt time.Time `json:"lastUsedAt,omitempty"`
}

type passkeyChallenge struct {
	Kind      string // "reg" | "auth"
	Challenge string // base64url
	RPID      string
	Origin    string
	ExpiresAt time.Time
}

var (
	pkChalMu    sync.Mutex
	pkChals     = map[string]passkeyChallenge{}
	b64u        = base64.RawURLEncoding
	b64s        = base64.StdEncoding
)

func (s *Store) ensureUserHandle() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.UserHandle != "" {
		return s.UserHandle
	}
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	s.UserHandle = b64u.EncodeToString(b)
	_ = s.save()
	return s.UserHandle
}

func (s *Store) passkeysCopy() ([]Passkey, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Passkey, len(s.Passkeys))
	copy(out, s.Passkeys)
	return out, s.PasskeyOnly && len(s.Passkeys) > 0
}

func (s *Store) hasPasskeys() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.Passkeys) > 0
}

func (s *Store) isPasskeyOnly() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.PasskeyOnly && len(s.Passkeys) > 0
}

func (s *Store) addPasskey(pk Passkey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.Passkeys {
		if s.Passkeys[i].ID == pk.ID {
			s.Passkeys[i] = pk
			return s.save()
		}
	}
	s.Passkeys = append(s.Passkeys, pk)
	return s.save()
}

func (s *Store) deletePasskey(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.PasskeyOnly && len(s.Passkeys) <= 1 {
		return fmt.Errorf("disable Passkey-only mode before removing the last passkey")
	}
	found := false
	out := s.Passkeys[:0]
	for _, p := range s.Passkeys {
		if p.ID == id {
			found = true
			continue
		}
		out = append(out, p)
	}
	if !found {
		return fmt.Errorf("passkey not found")
	}
	s.Passkeys = out
	if len(s.Passkeys) == 0 {
		s.PasskeyOnly = false
	}
	return s.save()
}

func (s *Store) setPasskeyOnly(on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if on && len(s.Passkeys) == 0 {
		return fmt.Errorf("register at least one passkey before enabling Passkey-only mode")
	}
	s.PasskeyOnly = on
	return s.save()
}

func (s *Store) findPasskey(id string) (Passkey, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.Passkeys {
		if p.ID == id {
			return p, true
		}
	}
	return Passkey{}, false
}

func (s *Store) touchPasskey(id string, signCount uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.Passkeys {
		if s.Passkeys[i].ID == id {
			if signCount > s.Passkeys[i].SignCount {
				s.Passkeys[i].SignCount = signCount
			}
			s.Passkeys[i].LastUsedAt = time.Now()
			_ = s.save()
			return
		}
	}
}

func issueChallenge(kind, rpID, origin string) string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	ch := b64u.EncodeToString(b)
	now := time.Now()
	pkChalMu.Lock()
	for k, v := range pkChals {
		if now.After(v.ExpiresAt) {
			delete(pkChals, k)
		}
	}
	pkChals[ch] = passkeyChallenge{
		Kind:      kind,
		Challenge: ch,
		RPID:      rpID,
		Origin:    origin,
		ExpiresAt: now.Add(5 * time.Minute),
	}
	pkChalMu.Unlock()
	return ch
}

func consumeChallenge(ch, kind string) (passkeyChallenge, bool) {
	pkChalMu.Lock()
	defer pkChalMu.Unlock()
	v, ok := pkChals[ch]
	if !ok {
		return passkeyChallenge{}, false
	}
	delete(pkChals, ch)
	if v.Kind != kind || time.Now().After(v.ExpiresAt) {
		return passkeyChallenge{}, false
	}
	return v, true
}

func requestRPAndOrigin(r *http.Request) (rpID, origin string) {
	rpID = requestHostOnly(r.Host)
	scheme := "https"
	if r.TLS == nil {
		if fp := r.Header.Get("X-Forwarded-Proto"); fp == "https" {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	origin = scheme + "://" + r.Host
	if o := strings.TrimSpace(r.Header.Get("Origin")); o != "" {
		// Origin must match the host we are serving on.
		if strings.EqualFold(o, origin) {
			origin = o
		}
	}
	return rpID, origin
}

// ---------- HTTP handlers ----------

func handlePasskeyList(w http.ResponseWriter, r *http.Request) {
	list, only := store.passkeysCopy()
	type pubRow struct {
		ID         string    `json:"id"`
		Name       string    `json:"name"`
		RPID       string    `json:"rpId"`
		Alg        string    `json:"alg"`
		Transports []string  `json:"transports,omitempty"`
		CreatedAt  time.Time `json:"createdAt"`
		LastUsedAt time.Time `json:"lastUsedAt,omitempty"`
	}
	rows := make([]pubRow, 0, len(list))
	for _, p := range list {
		rows = append(rows, pubRow{
			ID:         p.ID,
			Name:       p.Name,
			RPID:       p.RPID,
			Alg:        algName(p.Alg),
			Transports: p.Transports,
			CreatedAt:  p.CreatedAt,
			LastUsedAt: p.LastUsedAt,
		})
	}
	writeJSON(w, map[string]any{"passkeys": rows, "passkeyOnly": only})
}

func algName(alg int) string {
	switch alg {
	case -7:
		return "ES256"
	case -257:
		return "RS256"
	case -8:
		return "EdDSA"
	}
	return fmt.Sprintf("COSE(%d)", alg)
}

func handlePasskeyDelete(w http.ResponseWriter, r *http.Request) {
	var q struct {
		ID string `json:"id"`
	}
	if err := readJSON(r, &q); err != nil || strings.TrimSpace(q.ID) == "" {
		httpErr(w, 400, "invalid passkey ID")
		return
	}
	if err := store.deletePasskey(strings.TrimSpace(q.ID)); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func handlePasskeyMode(w http.ResponseWriter, r *http.Request) {
	var q struct {
		PasskeyOnly bool `json:"passkeyOnly"`
	}
	if err := readJSON(r, &q); err != nil {
		httpErr(w, 400, "bad request")
		return
	}
	if err := store.setPasskeyOnly(q.PasskeyOnly); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "passkeyOnly": store.isPasskeyOnly()})
}

func handlePasskeyRegBegin(w http.ResponseWriter, r *http.Request) {
	rpID, origin := requestRPAndOrigin(r)
	if rpID == "" {
		httpErr(w, 400, "cannot determine RP ID")
		return
	}
	ch := issueChallenge("reg", rpID, origin)
	pks, _ := store.passkeysCopy()
	excl := make([]map[string]any, 0, len(pks))
	for _, p := range pks {
		item := map[string]any{"type": "public-key", "id": p.ID}
		if len(p.Transports) > 0 {
			item["transports"] = p.Transports
		}
		excl = append(excl, item)
	}
	writeJSON(w, map[string]any{
		"challenge": ch,
		"rp":        map[string]string{"name": "Azure Cloud Console", "id": rpID},
		"user": map[string]string{
			"id":          store.ensureUserHandle(),
			"name":        "admin@" + rpID,
			"displayName": "Administrator",
		},
		"pubKeyCredParams": []map[string]any{
			{"type": "public-key", "alg": -7},
			{"type": "public-key", "alg": -257},
			{"type": "public-key", "alg": -8},
		},
		"timeout":     120000,
		"attestation": "none",
		"authenticatorSelection": map[string]any{
			"residentKey":        "preferred",
			"requireResidentKey": false,
			"userVerification":   "preferred",
		},
		"excludeCredentials": excl,
	})
}

func handlePasskeyRegFinish(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string   `json:"name"`
		ID       string   `json:"id"`
		Response struct {
			ClientDataJSON    string   `json:"clientDataJSON"`
			AttestationObject string   `json:"attestationObject"`
			Transports        []string `json:"transports"`
		} `json:"response"`
	}
	if err := readJSON(r, &req); err != nil {
		httpErr(w, 400, "bad request")
		return
	}
	clientDataRaw, err := b64u.DecodeString(req.Response.ClientDataJSON)
	if err != nil {
		httpErr(w, 400, "invalid clientDataJSON")
		return
	}
	var cd struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
		Origin    string `json:"origin"`
	}
	if err := json.Unmarshal(clientDataRaw, &cd); err != nil {
		httpErr(w, 400, "malformed clientDataJSON")
		return
	}
	if cd.Type != "webauthn.create" {
		httpErr(w, 400, "unexpected WebAuthn ceremony type")
		return
	}
	chal, ok := consumeChallenge(cd.Challenge, "reg")
	if !ok {
		httpErr(w, 400, "registration challenge expired or invalid")
		return
	}
	if !strings.EqualFold(cd.Origin, chal.Origin) {
		httpErr(w, 400, fmt.Sprintf("origin mismatch: got %q, want %q", cd.Origin, chal.Origin))
		return
	}

	attRaw, err := b64u.DecodeString(req.Response.AttestationObject)
	if err != nil {
		httpErr(w, 400, "invalid attestationObject")
		return
	}
	pk, err := parseAttestation(attRaw, chal.RPID)
	if err != nil {
		httpErr(w, 400, "attestation verification failed: "+err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Passkey " + time.Now().Format("2006-01-02")
	}
	if len(name) > 60 {
		name = name[:60]
	}
	pk.Name = name
	pk.Transports = req.Response.Transports
	pk.CreatedAt = time.Now()
	if err := store.addPasskey(pk); err != nil {
		httpErr(w, 500, "failed to save passkey: "+err.Error())
		return
	}
	log.Printf("passkey registered: %q (%s, alg=%s)", pk.Name, pk.RPID, algName(pk.Alg))
	writeJSON(w, map[string]any{"ok": true, "id": pk.ID, "name": pk.Name, "alg": algName(pk.Alg)})
}

func handlePasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if d := lockedOut(ip); d > 0 {
		httpErr(w, http.StatusTooManyRequests, "Too many attempts. Try again in "+d.Round(time.Second).String())
		return
	}
	pks, _ := store.passkeysCopy()
	if len(pks) == 0 {
		httpErr(w, 400, "No passkeys registered")
		return
	}
	rpID, origin := requestRPAndOrigin(r)
	ch := issueChallenge("auth", rpID, origin)
	allow := make([]map[string]any, 0, len(pks))
	for _, p := range pks {
		item := map[string]any{"type": "public-key", "id": p.ID}
		if len(p.Transports) > 0 {
			item["transports"] = p.Transports
		}
		allow = append(allow, item)
	}
	writeJSON(w, map[string]any{
		"challenge":        ch,
		"rpId":             rpID,
		"timeout":          120000,
		"userVerification": "preferred",
		"allowCredentials": allow,
	})
}

func handlePasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if d := lockedOut(ip); d > 0 {
		httpErr(w, http.StatusTooManyRequests, "Too many attempts. Try again in "+d.Round(time.Second).String())
		return
	}
	var req struct {
		ID       string `json:"id"`
		Response struct {
			ClientDataJSON    string `json:"clientDataJSON"`
			AuthenticatorData string `json:"authenticatorData"`
			Signature         string `json:"signature"`
		} `json:"response"`
	}
	if err := readJSON(r, &req); err != nil {
		httpErr(w, 400, "bad request")
		return
	}
	pk, ok := store.findPasskey(req.ID)
	if !ok {
		recordFail(ip)
		recordPanelLogin(ip, false)
		httpErr(w, http.StatusUnauthorized, "Unknown passkey")
		return
	}
	clientDataRaw, err := b64u.DecodeString(req.Response.ClientDataJSON)
	if err != nil {
		httpErr(w, 400, "invalid clientDataJSON")
		return
	}
	authData, err := b64u.DecodeString(req.Response.AuthenticatorData)
	if err != nil {
		httpErr(w, 400, "invalid authenticatorData")
		return
	}
	sig, err := b64u.DecodeString(req.Response.Signature)
	if err != nil {
		httpErr(w, 400, "invalid signature")
		return
	}

	var cd struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
		Origin    string `json:"origin"`
	}
	if err := json.Unmarshal(clientDataRaw, &cd); err != nil || cd.Type != "webauthn.get" {
		recordFail(ip)
		httpErr(w, 400, "invalid WebAuthn assertion")
		return
	}
	chal, ok := consumeChallenge(cd.Challenge, "auth")
	if !ok {
		recordFail(ip)
		httpErr(w, http.StatusUnauthorized, "Passkey challenge expired or invalid")
		return
	}
	if !strings.EqualFold(cd.Origin, chal.Origin) {
		recordFail(ip)
		httpErr(w, http.StatusUnauthorized, "Origin mismatch")
		return
	}

	signCount, err := verifyAssertion(pk, authData, clientDataRaw, sig)
	if err != nil {
		recordFail(ip)
		recordPanelLogin(ip, false)
		log.Printf("passkey login failed from %s (%s): %v", ip, pk.Name, err)
		httpErr(w, http.StatusUnauthorized, "Passkey verification failed: "+err.Error())
		return
	}

	store.touchPasskey(pk.ID, signCount)
	clearFail(ip)
	recordPanelLogin(ip, true)
	setSessionCookie(w, r, newSession(), int(sessionTTL.Seconds()))
	log.Printf("passkey login succeeded from %s (%s)", ip, pk.Name)
	writeJSON(w, map[string]bool{"ok": true})
}

// ---------- WebAuthn attestation & assertion crypto ----------

func parseAttestation(attRaw []byte, rpID string) (Passkey, error) {
	val, _, err := cborDecode(attRaw, 0)
	if err != nil {
		return Passkey{}, fmt.Errorf("cbor decode: %w", err)
	}
	m, ok := val.(map[any]any)
	if !ok {
		return Passkey{}, fmt.Errorf("attestationObject is not a CBOR map")
	}
	authData, ok := m["authData"].([]byte)
	if !ok || len(authData) < 55 {
		return Passkey{}, fmt.Errorf("missing or truncated authData")
	}
	wantHash := sha256.Sum256([]byte(rpID))
	if subtle.ConstantTimeCompare(authData[:32], wantHash[:]) != 1 {
		return Passkey{}, fmt.Errorf("RP ID hash mismatch")
	}
	flags := authData[32]
	if flags&0x01 == 0 {
		return Passkey{}, fmt.Errorf("user presence flag not set")
	}
	if flags&0x40 == 0 {
		return Passkey{}, fmt.Errorf("attested credential data flag not set")
	}
	signCount := binary.BigEndian.Uint32(authData[33:37])
	credIDLen := int(binary.BigEndian.Uint16(authData[53:55]))
	if credIDLen == 0 || 55+credIDLen >= len(authData) {
		return Passkey{}, fmt.Errorf("invalid credential ID length")
	}
	credID := authData[55 : 55+credIDLen]
	coseBytes := authData[55+credIDLen:]

	coseVal, _, err := cborDecode(coseBytes, 0)
	if err != nil {
		return Passkey{}, fmt.Errorf("COSE key decode: %w", err)
	}
	coseMap, ok := coseVal.(map[any]any)
	if !ok {
		return Passkey{}, fmt.Errorf("COSE key is not a CBOR map")
	}
	pub, alg, err := coseToPublicKey(coseMap)
	if err != nil {
		return Passkey{}, err
	}
	spki, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return Passkey{}, fmt.Errorf("marshal public key: %w", err)
	}
	return Passkey{
		ID:        b64u.EncodeToString(credID),
		RPID:      rpID,
		Alg:       alg,
		PublicKey: b64s.EncodeToString(spki),
		SignCount: signCount,
	}, nil
}

func verifyAssertion(pk Passkey, authData, clientDataRaw, sig []byte) (uint32, error) {
	if len(authData) < 37 {
		return 0, fmt.Errorf("truncated authenticatorData")
	}
	wantHash := sha256.Sum256([]byte(pk.RPID))
	if subtle.ConstantTimeCompare(authData[:32], wantHash[:]) != 1 {
		return 0, fmt.Errorf("RP ID hash mismatch")
	}
	if authData[32]&0x01 == 0 {
		return 0, fmt.Errorf("user presence flag not set")
	}
	signCount := binary.BigEndian.Uint32(authData[33:37])
	if pk.SignCount > 0 && signCount > 0 && signCount <= pk.SignCount {
		return 0, fmt.Errorf("authenticator sign count did not increase")
	}

	spki, err := b64s.DecodeString(pk.PublicKey)
	if err != nil {
		return 0, fmt.Errorf("corrupt stored public key")
	}
	pub, err := x509.ParsePKIXPublicKey(spki)
	if err != nil {
		return 0, fmt.Errorf("parse public key: %w", err)
	}

	cdHash := sha256.Sum256(clientDataRaw)
	signed := make([]byte, 0, len(authData)+len(cdHash))
	signed = append(signed, authData...)
	signed = append(signed, cdHash[:]...)

	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		digest := sha256.Sum256(signed)
		if !ecdsa.VerifyASN1(k, digest[:], sig) {
			return 0, fmt.Errorf("invalid ECDSA signature")
		}
	case *rsa.PublicKey:
		digest := sha256.Sum256(signed)
		if err := rsa.VerifyPKCS1v15(k, crypto.SHA256, digest[:], sig); err != nil {
			return 0, fmt.Errorf("invalid RSA signature")
		}
	case ed25519.PublicKey:
		if !ed25519.Verify(k, signed, sig) {
			return 0, fmt.Errorf("invalid Ed25519 signature")
		}
	default:
		return 0, fmt.Errorf("unsupported key type")
	}
	return signCount, nil
}

func coseInt(m map[any]any, k int64) (int64, bool) {
	v, ok := m[k]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case int64:
		return n, true
	case uint64:
		return int64(n), true
	}
	return 0, false
}

func coseBytes(m map[any]any, k int64) ([]byte, bool) {
	v, ok := m[k]
	if !ok {
		return nil, false
	}
	b, ok := v.([]byte)
	return b, ok
}

func coseToPublicKey(m map[any]any) (crypto.PublicKey, int, error) {
	kty, ok := coseInt(m, 1)
	if !ok {
		return nil, 0, fmt.Errorf("missing COSE kty")
	}
	alg, ok := coseInt(m, 3)
	if !ok {
		return nil, 0, fmt.Errorf("missing COSE alg")
	}
	switch kty {
	case 2: // EC2
		crv, _ := coseInt(m, -1)
		if crv != 1 || alg != -7 {
			return nil, 0, fmt.Errorf("unsupported EC curve/alg (%d/%d)", crv, alg)
		}
		x, okX := coseBytes(m, -2)
		y, okY := coseBytes(m, -3)
		if !okX || !okY || len(x) != 32 || len(y) != 32 {
			return nil, 0, fmt.Errorf("invalid EC2 coordinates")
		}
		curve := elliptic.P256()
		pub := &ecdsa.PublicKey{
			Curve: curve,
			X:     new(big.Int).SetBytes(x),
			Y:     new(big.Int).SetBytes(y),
		}
		if !curve.IsOnCurve(pub.X, pub.Y) {
			return nil, 0, fmt.Errorf("EC2 point not on P-256 curve")
		}
		return pub, int(alg), nil

	case 3: // RSA
		if alg != -257 {
			return nil, 0, fmt.Errorf("unsupported RSA alg %d", alg)
		}
		n, okN := coseBytes(m, -1)
		e, okE := coseBytes(m, -2)
		if !okN || !okE || len(n) < 128 || len(e) == 0 || len(e) > 8 {
			return nil, 0, fmt.Errorf("invalid RSA parameters")
		}
		ev := int(new(big.Int).SetBytes(e).Int64())
		if ev < 3 {
			return nil, 0, fmt.Errorf("invalid RSA exponent")
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: ev}, int(alg), nil

	case 1: // OKP (Ed25519)
		crv, _ := coseInt(m, -1)
		if crv != 6 || alg != -8 {
			return nil, 0, fmt.Errorf("unsupported OKP curve/alg (%d/%d)", crv, alg)
		}
		x, okX := coseBytes(m, -2)
		if !okX || len(x) != ed25519.PublicKeySize {
			return nil, 0, fmt.Errorf("invalid Ed25519 public key")
		}
		return ed25519.PublicKey(x), int(alg), nil
	}
	return nil, 0, fmt.Errorf("unsupported COSE key type %d", kty)
}

// ---------- Minimal RFC 8949 CBOR decoder (for WebAuthn attestationObject & COSE keys) ----------

func cborUint(b []byte, pos int, ai byte) (uint64, int, error) {
	if ai < 24 {
		return uint64(ai), pos, nil
	}
	switch ai {
	case 24:
		if pos+1 > len(b) {
			return 0, pos, fmt.Errorf("unexpected EOF")
		}
		return uint64(b[pos]), pos + 1, nil
	case 25:
		if pos+2 > len(b) {
			return 0, pos, fmt.Errorf("unexpected EOF")
		}
		return uint64(binary.BigEndian.Uint16(b[pos : pos+2])), pos + 2, nil
	case 26:
		if pos+4 > len(b) {
			return 0, pos, fmt.Errorf("unexpected EOF")
		}
		return uint64(binary.BigEndian.Uint32(b[pos : pos+4])), pos + 4, nil
	case 27:
		if pos+8 > len(b) {
			return 0, pos, fmt.Errorf("unexpected EOF")
		}
		return binary.BigEndian.Uint64(b[pos : pos+8]), pos + 8, nil
	default:
		return 0, pos, fmt.Errorf("unsupported CBOR additional info %d", ai)
	}
}

func cborDecode(b []byte, pos int) (any, int, error) {
	if pos >= len(b) {
		return nil, pos, fmt.Errorf("unexpected EOF")
	}
	hdr := b[pos]
	pos++
	major := hdr >> 5
	ai := hdr & 0x1f

	switch major {
	case 0: // unsigned int -> store as int64 for uniform COSE map lookups
		u, next, err := cborUint(b, pos, ai)
		if err != nil {
			return nil, next, err
		}
		return int64(u), next, nil
	case 1: // negative int: -1 - u
		u, next, err := cborUint(b, pos, ai)
		if err != nil {
			return nil, next, err
		}
		return -1 - int64(u), next, nil
	case 2: // byte string
		n, next, err := cborUint(b, pos, ai)
		if err != nil {
			return nil, next, err
		}
		end := next + int(n)
		if end < next || end > len(b) {
			return nil, next, fmt.Errorf("byte string out of bounds")
		}
		cp := make([]byte, int(n))
		copy(cp, b[next:end])
		return cp, end, nil
	case 3: // text string
		n, next, err := cborUint(b, pos, ai)
		if err != nil {
			return nil, next, err
		}
		end := next + int(n)
		if end < next || end > len(b) {
			return nil, next, fmt.Errorf("text string out of bounds")
		}
		return string(b[next:end]), end, nil
	case 4: // array
		n, next, err := cborUint(b, pos, ai)
		if err != nil {
			return nil, next, err
		}
		if n > 10000 {
			return nil, next, fmt.Errorf("array too large")
		}
		arr := make([]any, 0, int(n))
		for i := uint64(0); i < n; i++ {
			var v any
			v, next, err = cborDecode(b, next)
			if err != nil {
				return nil, next, err
			}
			arr = append(arr, v)
		}
		return arr, next, nil
	case 5: // map
		n, next, err := cborUint(b, pos, ai)
		if err != nil {
			return nil, next, err
		}
		if n > 10000 {
			return nil, next, fmt.Errorf("map too large")
		}
		mp := make(map[any]any, int(n))
		for i := uint64(0); i < n; i++ {
			var k, v any
			k, next, err = cborDecode(b, next)
			if err != nil {
				return nil, next, err
			}
			v, next, err = cborDecode(b, next)
			if err != nil {
				return nil, next, err
			}
			// Only comparable keys (int64, string, bool) can be map keys in Go
			switch k.(type) {
			case int64, uint64, string, bool:
				mp[k] = v
			}
		}
		return mp, next, nil
	case 6: // tag — unwrap
		_, next, err := cborUint(b, pos, ai)
		if err != nil {
			return nil, next, err
		}
		return cborDecode(b, next)
	case 7: // simple / float
		switch ai {
		case 20:
			return false, pos, nil
		case 21:
			return true, pos, nil
		case 22, 23:
			return nil, pos, nil
		case 24:
			if pos+1 > len(b) {
				return nil, pos, fmt.Errorf("unexpected EOF")
			}
			return b[pos], pos + 1, nil
		case 25:
			return nil, pos + 2, nil
		case 26:
			return nil, pos + 4, nil
		case 27:
			return nil, pos + 8, nil
		}
		return nil, pos, fmt.Errorf("unsupported CBOR simple value %d", ai)
	}
	return nil, pos, fmt.Errorf("unknown CBOR major type %d", major)
}
