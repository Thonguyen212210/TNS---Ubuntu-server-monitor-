package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"testing"
)

func cborEncodeHdr(major byte, n uint64) []byte {
	m := major << 5
	switch {
	case n < 24:
		return []byte{m | byte(n)}
	case n < 256:
		return []byte{m | 24, byte(n)}
	case n < 65536:
		return []byte{m | 25, byte(n >> 8), byte(n)}
	default:
		var b [5]byte
		b[0] = m | 26
		binary.BigEndian.PutUint32(b[1:], uint32(n))
		return b[:]
	}
}

func cborEncodeInt(v int64) []byte {
	if v >= 0 {
		return cborEncodeHdr(0, uint64(v))
	}
	return cborEncodeHdr(1, uint64(-1-v))
}

func cborEncodeBytes(b []byte) []byte {
	return append(cborEncodeHdr(2, uint64(len(b))), b...)
}

func cborEncodeText(s string) []byte {
	return append(cborEncodeHdr(3, uint64(len(s))), []byte(s)...)
}

func TestPasskeyAttestationAndAssertionES256(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	xBytes := make([]byte, 32)
	yBytes := make([]byte, 32)
	priv.X.FillBytes(xBytes)
	priv.Y.FillBytes(yBytes)

	// Build COSE EC2 P-256 map: {1: 2, 3: -7, -1: 1, -2: x, -3: y}
	cose := cborEncodeHdr(5, 5)
	cose = append(cose, cborEncodeInt(1)...)
	cose = append(cose, cborEncodeInt(2)...)
	cose = append(cose, cborEncodeInt(3)...)
	cose = append(cose, cborEncodeInt(-7)...)
	cose = append(cose, cborEncodeInt(-1)...)
	cose = append(cose, cborEncodeInt(1)...)
	cose = append(cose, cborEncodeInt(-2)...)
	cose = append(cose, cborEncodeBytes(xBytes)...)
	cose = append(cose, cborEncodeInt(-3)...)
	cose = append(cose, cborEncodeBytes(yBytes)...)

	rpID := "tnslab.eastasia.cloudapp.azure.com"
	rpHash := sha256.Sum256([]byte(rpID))
	credID := []byte("test-credential-id-001")

	var authData []byte
	authData = append(authData, rpHash[:]...)
	authData = append(authData, 0x45) // UP | UV | AT
	authData = append(authData, 0, 0, 0, 1)
	authData = append(authData, make([]byte, 16)...) // AAGUID
	authData = append(authData, byte(len(credID)>>8), byte(len(credID)))
	authData = append(authData, credID...)
	authData = append(authData, cose...)

	// Build attestationObject CBOR: {"fmt": "none", "attStmt": {}, "authData": authData}
	att := cborEncodeHdr(5, 3)
	att = append(att, cborEncodeText("fmt")...)
	att = append(att, cborEncodeText("none")...)
	att = append(att, cborEncodeText("attStmt")...)
	att = append(att, cborEncodeHdr(5, 0)...)
	att = append(att, cborEncodeText("authData")...)
	att = append(att, cborEncodeBytes(authData)...)

	pk, err := parseAttestation(att, rpID)
	if err != nil {
		t.Fatalf("parseAttestation: %v", err)
	}
	if pk.Alg != -7 || pk.ID != b64u.EncodeToString(credID) || pk.SignCount != 1 {
		t.Fatalf("unexpected passkey: %+v", pk)
	}

	// Verify assertion with incrementing signCount=2
	var assertAuth []byte
	assertAuth = append(assertAuth, rpHash[:]...)
	assertAuth = append(assertAuth, 0x05) // UP | UV
	assertAuth = append(assertAuth, 0, 0, 0, 2)

	clientData := []byte(`{"type":"webauthn.get","challenge":"abc","origin":"https://tnslab.eastasia.cloudapp.azure.com"}`)
	cdHash := sha256.Sum256(clientData)
	signed := append(append([]byte{}, assertAuth...), cdHash[:]...)
	digest := sha256.Sum256(signed)
	sig, err := ecdsa.SignASN1(rand.Reader, priv, digest[:])
	if err != nil {
		t.Fatal(err)
	}

	sc, err := verifyAssertion(pk, assertAuth, clientData, sig)
	if err != nil {
		t.Fatalf("verifyAssertion failed: %v", err)
	}
	if sc != 2 {
		t.Fatalf("signCount got %d want 2", sc)
	}

	// Reject tampered signature
	sig[len(sig)-1] ^= 0xff
	if _, err := verifyAssertion(pk, assertAuth, clientData, sig); err == nil {
		t.Fatalf("expected tampered signature to fail")
	}

	// Reject stale signCount (clone detection)
	pk.SignCount = 5
	sig[len(sig)-1] ^= 0xff
	if _, err := verifyAssertion(pk, assertAuth, clientData, sig); err == nil {
		t.Fatalf("expected non-incrementing signCount to fail")
	}
}
