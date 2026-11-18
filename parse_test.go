package main

import (
	"testing"
	"time"
)

func TestParseAuthLine(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.Local)
	cases := []struct {
		line string
		ok   bool
		user string
		ip   string
		good bool
	}{
		{"Nov 18 10:01:02 srv sshd[123]: Accepted publickey for ops from 203.0.113.5 port 5122 ssh2: ED25519 SHA256:x", true, "ops", "203.0.113.5", true},
		{"Nov  8 10:01:02 srv sshd[123]: Failed password for root from 203.0.113.6 port 5122 ssh2", true, "root", "203.0.113.6", false},
		{"Nov 18 10:01:02 srv sshd[123]: Failed password for invalid user admin from 203.0.113.7 port 5122 ssh2", false, "", "", false},
		{"Nov 18 10:01:02 srv sshd[123]: Invalid user admin from 203.0.113.7 port 5122", true, "admin", "203.0.113.7", false},
		{"2026-11-18T10:01:02.123456+07:00 srv sshd-session[99]: Accepted password for deploy from 2001:db8::1 port 22 ssh2", true, "deploy", "2001:db8::1", true},
		{"2026-11-18T10:01:02+0700 srv sshd[99]: Connection closed by authenticating user root 198.51.100.3 port 40000 [preauth]", true, "root", "198.51.100.3", false},
		{"Nov 18 10:01:02 srv CRON[1]: pam_unix(cron:session): session opened", false, "", "", false},
	}
	for _, c := range cases {
		ev, ok := parseAuthLine(c.line, now)
		if ok != c.ok {
			t.Fatalf("%q: ok=%v want %v", c.line, ok, c.ok)
		}
		if ok && (ev.User != c.user || ev.IP != c.ip || ev.OK != c.good) {
			t.Fatalf("%q: got %+v", c.line, ev)
		}
	}
	// syslog dates from "December" seen in January belong to last year
	jan := time.Date(2027, 1, 2, 0, 0, 0, 0, time.Local)
	ev, _ := parseAuthLine("Dec 31 23:00:00 srv sshd[1]: Failed password for root from 203.0.113.9 port 1 ssh2", jan)
	if ev.T.Year() != 2026 {
		t.Fatalf("year rollover: %v", ev.T)
	}
}

func TestParseUfw(t *testing.T) {
	status := `Status: active

     To                         Action      From
     --                         ------      ----
[ 1] Anywhere                   DENY IN     203.0.113.4
[ 2] 22/tcp                     ALLOW IN    Anywhere                   # ssh (tns safeguard)
[ 3] 1611/tcp                   ALLOW IN    Anywhere
[ 4] 8000:8100/udp              LIMIT IN    10.0.0.0/8
[ 5] 22/tcp (v6)                ALLOW IN    Anywhere (v6)
`
	verbose := "Status: active\nLogging: on (low)\nDefault: deny (incoming), allow (outgoing), disabled (routed)\n"
	st := parseUfw(status, verbose)
	if !st.Active || len(st.Rules) != 5 {
		t.Fatalf("got %+v", st)
	}
	if st.Rules[1].Comment != "ssh (tns safeguard)" || st.Rules[1].To != "22/tcp" || st.Rules[1].Action != "ALLOW IN" {
		t.Fatalf("rule2 %+v", st.Rules[1])
	}
	if st.Rules[3].From != "10.0.0.0/8" || st.Rules[3].Action != "LIMIT IN" {
		t.Fatalf("rule4 %+v", st.Rules[3])
	}
	if !st.Rules[4].V6 || st.Default == "" {
		t.Fatalf("rule5 %+v default %q", st.Rules[4], st.Default)
	}
}

func TestFwValidate(t *testing.T) {
	bad := []fwReq{
		{Op: "add", Action: "allow", Port: "80; rm -rf /"},
		{Op: "add", Action: "allow", From: "1.2.3.4 && x"},
		{Op: "add", Action: "boom", Port: "80"},
		{Op: "add", Action: "allow"},
		{Op: "add", Action: "allow", Port: "8000:8100"},
	}
	for _, q := range bad {
		if q.validate() == nil {
			t.Fatalf("expected invalid: %+v", q)
		}
	}
	good := fwReq{Op: "add", Action: "deny", From: "203.0.113.0/24", Comment: "chặn bot"}
	if err := good.validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSplitArgs(t *testing.T) {
	a := splitArgs(`-A INPUT -s 1.2.3.4/32 -m comment --comment "bad bot" -j DROP`)
	if len(a) != 10 || a[7] != "bad bot" {
		t.Fatalf("%q", a)
	}
}
