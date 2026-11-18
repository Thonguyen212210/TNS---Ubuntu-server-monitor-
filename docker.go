package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var reContainerID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

func dockerSocket() string {
	if h := os.Getenv("DOCKER_HOST"); strings.HasPrefix(h, "unix://") {
		return strings.TrimPrefix(h, "unix://")
	}
	home, _ := os.UserHomeDir()
	for _, p := range []string{"/var/run/docker.sock", "/run/docker.sock", filepath.Join(home, ".docker/run/docker.sock"),
		filepath.Join(home, ".colima/default/docker.sock"), filepath.Join(home, ".orbstack/run/docker.sock")} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func dockerClient(sock string, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}},
	}
}

func dockerDo(method, path string, timeout time.Duration) ([]byte, int, error) {
	sock := dockerSocket()
	if sock == "" {
		return nil, 0, fmt.Errorf("docker socket not found")
	}
	req, _ := http.NewRequest(method, "http://docker"+path, nil)
	resp, err := dockerClient(sock, timeout).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	return b, resp.StatusCode, err
}

type Container struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Image   string   `json:"image"`
	State   string   `json:"state"`
	Status  string   `json:"status"`
	Created int64    `json:"created"`
	Ports   []string `json:"ports"`
	Compose string   `json:"compose,omitempty"`
	CPU     float64  `json:"cpu"`
	Mem     uint64   `json:"mem"`
	MemLim  uint64   `json:"memLimit"`
}

type Image struct {
	ID      string   `json:"id"`
	Tags    []string `json:"tags"`
	Size    int64    `json:"size"`
	Created int64    `json:"created"`
}

func handleDockerList(w http.ResponseWriter, r *http.Request) {
	if dockerSocket() == "" {
		if *flagDemo {
			writeJSON(w, demoDocker())
			return
		}
		writeJSON(w, map[string]any{"available": false, "note": "Không tìm thấy Docker (docker.sock)."})
		return
	}
	b, code, err := dockerDo("GET", "/containers/json?all=1", 10*time.Second)
	if err != nil || code != 200 {
		msg := "Không kết nối được Docker"
		if err != nil {
			msg += ": " + err.Error()
		} else {
			msg += ": " + string(b)
		}
		writeJSON(w, map[string]any{"available": false, "note": msg})
		return
	}
	var raw []struct {
		Id, Image, State, Status string
		Names                    []string
		Created                  int64
		Labels                   map[string]string
		Ports                    []struct {
			IP          string
			PrivatePort int
			PublicPort  int
			Type        string
		}
	}
	_ = json.Unmarshal(b, &raw)
	out := make([]Container, len(raw))
	var wg sync.WaitGroup
	for i, c := range raw {
		ct := Container{ID: c.Id[:12], Image: c.Image, State: c.State, Status: c.Status, Created: c.Created,
			Compose: c.Labels["com.docker.compose.project"], Ports: []string{}}
		if len(c.Names) > 0 {
			ct.Name = strings.TrimPrefix(c.Names[0], "/")
		}
		seen := map[string]bool{}
		for _, p := range c.Ports {
			s := fmt.Sprintf("%d/%s", p.PrivatePort, p.Type)
			if p.PublicPort > 0 {
				s = fmt.Sprintf("%d→%d/%s", p.PublicPort, p.PrivatePort, p.Type)
			}
			if !seen[s] {
				seen[s] = true
				ct.Ports = append(ct.Ports, s)
			}
		}
		out[i] = ct
		if c.State == "running" {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				fillStats(&out[i])
			}(i)
		}
	}
	wg.Wait()

	var images []Image
	if ib, code, err := dockerDo("GET", "/images/json", 10*time.Second); err == nil && code == 200 {
		var ri []struct {
			Id       string
			RepoTags []string
			Size     int64
			Created  int64
		}
		_ = json.Unmarshal(ib, &ri)
		for _, i := range ri {
			images = append(images, Image{ID: strings.TrimPrefix(i.Id, "sha256:")[:12], Tags: i.RepoTags, Size: i.Size, Created: i.Created})
		}
	}
	var ver struct{ Version string }
	if vb, _, err := dockerDo("GET", "/version", 5*time.Second); err == nil {
		_ = json.Unmarshal(vb, &ver)
	}
	writeJSON(w, map[string]any{"available": true, "version": ver.Version, "containers": out, "images": images})
}

func fillStats(c *Container) {
	b, code, err := dockerDo("GET", "/containers/"+c.ID+"/stats?stream=false&one-shot=false", 6*time.Second)
	if err != nil || code != 200 {
		return
	}
	var s struct {
		CPUStats struct {
			CPUUsage struct {
				TotalUsage uint64 `json:"total_usage"`
			} `json:"cpu_usage"`
			SystemUsage uint64 `json:"system_cpu_usage"`
			OnlineCPUs  int    `json:"online_cpus"`
		} `json:"cpu_stats"`
		PreCPUStats struct {
			CPUUsage struct {
				TotalUsage uint64 `json:"total_usage"`
			} `json:"cpu_usage"`
			SystemUsage uint64 `json:"system_cpu_usage"`
		} `json:"precpu_stats"`
		MemoryStats struct {
			Usage uint64            `json:"usage"`
			Limit uint64            `json:"limit"`
			Stats map[string]uint64 `json:"stats"`
		} `json:"memory_stats"`
	}
	if json.Unmarshal(b, &s) != nil {
		return
	}
	cd := float64(s.CPUStats.CPUUsage.TotalUsage) - float64(s.PreCPUStats.CPUUsage.TotalUsage)
	sd := float64(s.CPUStats.SystemUsage) - float64(s.PreCPUStats.SystemUsage)
	if sd > 0 && cd > 0 {
		n := s.CPUStats.OnlineCPUs
		if n == 0 {
			n = 1
		}
		c.CPU = round1(cd / sd * float64(n) * 100)
	}
	c.Mem = s.MemoryStats.Usage
	if ic := s.MemoryStats.Stats["inactive_file"]; ic < c.Mem {
		c.Mem -= ic
	}
	c.MemLim = s.MemoryStats.Limit
}

func handleDockerAction(w http.ResponseWriter, r *http.Request) {
	var q struct{ ID, Op string }
	if err := readJSON(r, &q); err != nil || !reContainerID.MatchString(q.ID) {
		httpErr(w, 400, "bad request")
		return
	}
	if dockerSocket() == "" && *flagDemo {
		if err := demoDockerAction(q.ID, q.Op); err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
		return
	}
	var method, path string
	id := url.PathEscape(q.ID)
	switch q.Op {
	case "start", "stop", "restart", "pause", "unpause":
		method, path = "POST", "/containers/"+id+"/"+q.Op
	case "remove":
		method, path = "DELETE", "/containers/"+id+"?force=1"
	case "rmi":
		method, path = "DELETE", "/images/"+id
	default:
		httpErr(w, 400, "op không hợp lệ")
		return
	}
	b, code, err := dockerDo(method, path, 60*time.Second)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	if code >= 300 && code != 304 {
		var e struct{ Message string }
		_ = json.Unmarshal(b, &e)
		httpErr(w, code, e.Message)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func handleDockerLogs(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if !reContainerID.MatchString(id) {
		httpErr(w, 400, "bad id")
		return
	}
	if dockerSocket() == "" && *flagDemo {
		writeJSON(w, map[string]string{"logs": demoDockerLogs(id)})
		return
	}
	b, code, err := dockerDo("GET", "/containers/"+url.PathEscape(id)+"/logs?stdout=1&stderr=1&tail=400&timestamps=1", 20*time.Second)
	if err != nil || code != 200 {
		httpErr(w, 500, "không lấy được log")
		return
	}
	writeJSON(w, map[string]string{"logs": demuxDocker(b)})
}

// demuxDocker strips the 8-byte stream headers used when a container has no TTY.
func demuxDocker(b []byte) string {
	if len(b) < 8 || (b[0] != 1 && b[0] != 2) || b[1] != 0 || b[2] != 0 || b[3] != 0 {
		return string(b)
	}
	var sb strings.Builder
	for len(b) >= 8 {
		n := int(binary.BigEndian.Uint32(b[4:8]))
		b = b[8:]
		if n > len(b) {
			n = len(b)
		}
		sb.Write(b[:n])
		b = b[n:]
	}
	return sb.String()
}
