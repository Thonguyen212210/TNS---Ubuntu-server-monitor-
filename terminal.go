package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  8192,
	WriteBufferSize: 32768,
	CheckOrigin: func(r *http.Request) bool {
		if validPeerKey(r) {
			return true
		}
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		return strings.HasSuffix(origin, "://"+r.Host)
	},
}

func pickShell() string {
	if s := os.Getenv("SHELL"); s != "" {
		if _, err := os.Stat(s); err == nil {
			return s
		}
	}
	for _, s := range []string{"/bin/bash", "/usr/bin/bash", "/bin/zsh", "/bin/sh"} {
		if _, err := os.Stat(s); err == nil {
			return s
		}
	}
	return "/bin/sh"
}

func handleTerminal(w http.ResponseWriter, r *http.Request) {
	if id := targetNodeID(r); id != "" && id != "local" {
		peer, ok := getPeerNode(id)
		if !ok {
			httpErr(w, http.StatusNotFound, "server not found: "+id)
			return
		}
		proxyTerminalWS(w, r, peer)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	var cmd *exec.Cmd
	if id := r.URL.Query().Get("container"); id != "" {
		if !reContainerID.MatchString(id) || !hasCmd("docker") {
			_ = conn.WriteMessage(websocket.BinaryMessage, []byte("\r\n\x1b[31mDocker CLI unavailable or invalid container ID.\x1b[0m\r\n"))
			return
		}
		cmd = exec.Command("docker", "exec", "-it", "-e", "TERM=xterm-256color", id, "/bin/sh", "-c",
			"if command -v bash >/dev/null 2>&1; then exec bash; else exec sh; fi")
	} else {
		cmd = exec.Command(pickShell(), "-l")
		home, _ := os.UserHomeDir()
		cmd.Dir = home
		if cwd := r.URL.Query().Get("cwd"); cwd != "" {
			if fi, err := os.Stat(cwd); err == nil && fi.IsDir() {
				cmd.Dir = cwd
			}
		}
	}
	env := os.Environ()
	env = append(env, "TERM=xterm-256color", "COLORTERM=truecolor")
	if os.Getenv("LANG") == "" {
		env = append(env, "LANG=C.UTF-8")
	}
	cmd.Env = env

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 120, Rows: 32})
	if err != nil {
		_ = conn.WriteMessage(websocket.BinaryMessage, []byte("\r\nFailed to start shell: "+err.Error()+"\r\n"))
		return
	}
	defer func() {
		_ = ptmx.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGHUP)
			done := make(chan struct{})
			go func() { _ = cmd.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				_ = cmd.Process.Kill()
			}
		}
	}()

	var wmu sync.Mutex
	// pty -> websocket
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				wmu.Lock()
				werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n])
				wmu.Unlock()
				if werr != nil {
					return
				}
			}
			if err != nil {
				wmu.Lock()
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"t":"exit"}`))
				_ = conn.Close()
				wmu.Unlock()
				return
			}
		}
	}()

	// keepalive
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				wmu.Lock()
				_ = conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
				wmu.Unlock()
			}
		}
	}()

	// websocket -> pty
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if mt == websocket.BinaryMessage {
			if _, err := ptmx.Write(data); err != nil {
				return
			}
			continue
		}
		var ctl struct {
			T    string `json:"t"`
			Cols uint16 `json:"cols"`
			Rows uint16 `json:"rows"`
		}
		if json.Unmarshal(data, &ctl) == nil && ctl.T == "resize" && ctl.Cols > 0 && ctl.Rows > 0 {
			if err := pty.Setsize(ptmx, &pty.Winsize{Cols: ctl.Cols, Rows: ctl.Rows}); err != nil {
				log.Printf("pty resize: %v", err)
			}
		}
	}
}
