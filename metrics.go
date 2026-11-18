package main

import (
	"net/http"
	"os/user"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
	"github.com/shirou/gopsutil/v4/sensors"
)

type Sample struct {
	T     int64   `json:"t"`
	CPU   float64 `json:"cpu"`
	Mem   float64 `json:"mem"`
	Swap  float64 `json:"swap"`
	Load  float64 `json:"load"`
	NetRx float64 `json:"rx"`
	NetTx float64 `json:"tx"`
	DiskR float64 `json:"dr"`
	DiskW float64 `json:"dw"`
}

type DiskUsage struct {
	Mount  string  `json:"mount"`
	Device string  `json:"device"`
	FS     string  `json:"fs"`
	Total  uint64  `json:"total"`
	Used   uint64  `json:"used"`
	Pct    float64 `json:"pct"`
}

type Snapshot struct {
	Sample
	Cores    []float64   `json:"cores"`
	MemTotal uint64      `json:"memTotal"`
	MemUsed  uint64      `json:"memUsed"`
	MemAvail uint64      `json:"memAvail"`
	MemCache uint64      `json:"memCache"`
	SwapTot  uint64      `json:"swapTotal"`
	SwapUsed uint64      `json:"swapUsed"`
	Load5    float64     `json:"load5"`
	Load15   float64     `json:"load15"`
	Uptime   uint64      `json:"uptime"`
	Procs    int         `json:"procs"`
	Disks    []DiskUsage `json:"disks"`
	Temp     float64     `json:"temp"`
	NetRxTot uint64      `json:"rxTotal"`
	NetTxTot uint64      `json:"txTotal"`
}

const (
	shortCap = 300  // 2s × 300 = 10 min
	longCap  = 1440 // 1 min × 1440 = 24 h
	interval = 2 * time.Second
)

var (
	metMu    sync.RWMutex
	shortH   []Sample
	longH    []Sample
	current  Snapshot
	accum    []Sample
	diskList []DiskUsage
	diskAt   time.Time
)

func startCollector() {
	_, _ = cpu.Percent(0, true)
	go func() {
		var lastNet, lastDisk [2]uint64
		var lastT time.Time
		tick := time.NewTicker(interval)
		defer tick.Stop()
		for {
			now := time.Now()
			s := Snapshot{}
			s.T = now.UnixMilli()

			if cores, err := cpu.Percent(0, true); err == nil && len(cores) > 0 {
				sum := 0.0
				for i := range cores {
					cores[i] = round1(cores[i])
					sum += cores[i]
				}
				s.Cores = cores
				s.CPU = round1(sum / float64(len(cores)))
			}
			if vm, err := mem.VirtualMemory(); err == nil {
				s.MemTotal, s.MemAvail, s.MemCache = vm.Total, vm.Available, vm.Cached
				s.MemUsed = vm.Total - vm.Available
				s.Mem = round1(float64(s.MemUsed) / float64(vm.Total) * 100)
			}
			if sw, err := mem.SwapMemory(); err == nil {
				s.SwapTot, s.SwapUsed, s.Swap = sw.Total, sw.Used, round1(sw.UsedPercent)
			}
			if la, err := load.Avg(); err == nil {
				s.Load, s.Load5, s.Load15 = la.Load1, la.Load5, la.Load15
			}
			if up, err := host.Uptime(); err == nil {
				s.Uptime = up
			}

			var rx, tx, dr, dw uint64
			if ios, err := gnet.IOCounters(true); err == nil {
				for _, io := range ios {
					if io.Name == "lo" || strings.HasPrefix(io.Name, "lo0") || strings.HasPrefix(io.Name, "veth") ||
						strings.HasPrefix(io.Name, "docker") || strings.HasPrefix(io.Name, "br-") {
						continue
					}
					rx += io.BytesRecv
					tx += io.BytesSent
				}
			}
			if dios, err := disk.IOCounters(); err == nil {
				for name, d := range dios {
					if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") {
						continue
					}
					dr += d.ReadBytes
					dw += d.WriteBytes
				}
			}
			s.NetRxTot, s.NetTxTot = rx, tx
			if !lastT.IsZero() {
				dt := now.Sub(lastT).Seconds()
				s.NetRx = rate(rx, lastNet[0], dt)
				s.NetTx = rate(tx, lastNet[1], dt)
				s.DiskR = rate(dr, lastDisk[0], dt)
				s.DiskW = rate(dw, lastDisk[1], dt)
			}
			lastNet, lastDisk, lastT = [2]uint64{rx, tx}, [2]uint64{dr, dw}, now

			if time.Since(diskAt) > 30*time.Second {
				diskList = collectDisks()
				diskAt = now
				if pids, err := process.Pids(); err == nil {
					s.Procs = len(pids)
				}
				s.Temp = readTemp()
			} else {
				metMu.RLock()
				s.Temp = current.Temp
				s.Procs = current.Procs
				metMu.RUnlock()
			}
			s.Disks = diskList

			metMu.Lock()
			current = s
			shortH = appendCap(shortH, s.Sample, shortCap)
			accum = append(accum, s.Sample)
			if len(accum) >= 30 {
				longH = appendCap(longH, avgSamples(accum), longCap)
				accum = accum[:0]
			}
			metMu.Unlock()
			<-tick.C
		}
	}()
}

func rate(cur, prev uint64, dt float64) float64 {
	if cur < prev || dt <= 0 {
		return 0
	}
	return float64(cur-prev) / dt
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }

func appendCap(h []Sample, s Sample, c int) []Sample {
	h = append(h, s)
	if len(h) > c {
		h = h[len(h)-c:]
	}
	return h
}

func avgSamples(a []Sample) Sample {
	var o Sample
	n := float64(len(a))
	for _, s := range a {
		o.CPU += s.CPU
		o.Mem += s.Mem
		o.Swap += s.Swap
		o.Load += s.Load
		o.NetRx += s.NetRx
		o.NetTx += s.NetTx
		o.DiskR += s.DiskR
		o.DiskW += s.DiskW
	}
	o.T = a[len(a)-1].T
	o.CPU, o.Mem, o.Swap, o.Load = round1(o.CPU/n), round1(o.Mem/n), round1(o.Swap/n), o.Load/n
	o.NetRx, o.NetTx, o.DiskR, o.DiskW = o.NetRx/n, o.NetTx/n, o.DiskR/n, o.DiskW/n
	return o
}

var skipFS = map[string]bool{"tmpfs": true, "devtmpfs": true, "squashfs": true, "overlay": true, "devfs": true,
	"autofs": true, "nullfs": true, "proc": true, "sysfs": true, "cgroup": true, "cgroup2": true, "efivarfs": true}

func collectDisks() []DiskUsage {
	parts, err := disk.Partitions(false)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []DiskUsage
	for _, p := range parts {
		if skipFS[p.Fstype] || seen[p.Device] || strings.HasPrefix(p.Mountpoint, "/snap") ||
			strings.HasPrefix(p.Mountpoint, "/System/Volumes/") && p.Mountpoint != "/System/Volumes/Data" ||
			strings.HasPrefix(p.Mountpoint, "/boot/efi") || strings.HasPrefix(p.Mountpoint, "/Library/Developer") {
			continue
		}
		u, err := disk.Usage(p.Mountpoint)
		if err != nil || u.Total == 0 {
			continue
		}
		seen[p.Device] = true
		out = append(out, DiskUsage{Mount: p.Mountpoint, Device: p.Device, FS: p.Fstype, Total: u.Total, Used: u.Used, Pct: round1(u.UsedPercent)})
		if len(out) >= 8 {
			break
		}
	}
	return out
}

func readTemp() float64 {
	ts, err := sensors.SensorsTemperatures()
	if err != nil && len(ts) == 0 {
		return 0
	}
	best := 0.0
	for _, t := range ts {
		k := strings.ToLower(t.SensorKey)
		if strings.Contains(k, "package") || strings.Contains(k, "tctl") || strings.Contains(k, "coretemp") ||
			strings.Contains(k, "cpu") || strings.Contains(k, "k10temp") || strings.Contains(k, "soc") {
			if t.Temperature > best && t.Temperature < 130 {
				best = t.Temperature
			}
		}
	}
	return round1(best)
}

// ---------- handlers ----------

func handleSysInfo(w http.ResponseWriter, r *http.Request) {
	hi, _ := host.Info()
	ci, _ := cpu.Info()
	physical, _ := cpu.Counts(false)
	logical, _ := cpu.Counts(true)
	model := ""
	if len(ci) > 0 {
		model = ci[0].ModelName
	}
	var ips []map[string]string
	if ifs, err := gnet.Interfaces(); err == nil {
		for _, i := range ifs {
			if i.Name == "lo" || strings.HasPrefix(i.Name, "lo0") || strings.HasPrefix(i.Name, "veth") || strings.HasPrefix(i.Name, "docker") || strings.HasPrefix(i.Name, "br-") {
				continue
			}
			for _, a := range i.Addrs {
				if strings.HasPrefix(a.Addr, "fe80") {
					continue
				}
				ips = append(ips, map[string]string{"iface": i.Name, "addr": a.Addr})
			}
		}
	}
	out := map[string]any{
		"hostname": "", "os": runtime.GOOS, "arch": runtime.GOARCH, "cpuModel": model,
		"cores": physical, "threads": logical, "ips": ips,
	}
	if hi != nil {
		out["hostname"] = hi.Hostname
		out["platform"] = hi.Platform
		out["platformVersion"] = hi.PlatformVersion
		out["kernel"] = hi.KernelVersion
		out["virtualization"] = hi.VirtualizationSystem
		out["bootTime"] = hi.BootTime
	}
	writeJSON(w, out)
}

func handleSysStats(w http.ResponseWriter, r *http.Request) {
	metMu.RLock()
	defer metMu.RUnlock()
	out := map[string]any{"current": current}
	switch r.URL.Query().Get("history") {
	case "short":
		out["history"] = shortH
	case "long":
		out["history"] = longH
	}
	writeJSON(w, out)
}

// ---------- processes ----------

type procRow struct {
	PID    int32   `json:"pid"`
	Name   string  `json:"name"`
	User   string  `json:"user"`
	CPU    float64 `json:"cpu"`
	RSS    uint64  `json:"rss"`
	MemPct float64 `json:"mem"`
	Cmd    string  `json:"cmd"`
}

var (
	procMu   sync.Mutex
	procPrev = map[int32]float64{}
	procAt   time.Time
	userMemo = map[string]string{}
)

func lookupUser(uid int32) string {
	k := strconv.Itoa(int(uid))
	if n, ok := userMemo[k]; ok {
		return n
	}
	n := k
	if u, err := user.LookupId(k); err == nil {
		n = u.Username
	}
	userMemo[k] = n
	return n
}

func handleProcesses(w http.ResponseWriter, r *http.Request) {
	procMu.Lock()
	defer procMu.Unlock()
	procs, err := process.Processes()
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	vm, _ := mem.VirtualMemory()
	now := time.Now()
	dt := now.Sub(procAt).Seconds()
	next := make(map[int32]float64, len(procs))
	rows := make([]procRow, 0, len(procs))
	for _, p := range procs {
		t, err := p.Times()
		if err != nil {
			continue
		}
		total := t.User + t.System
		next[p.Pid] = total
		row := procRow{PID: p.Pid}
		if prev, ok := procPrev[p.Pid]; ok && dt > 0 && dt < 120 {
			row.CPU = round1((total - prev) / dt * 100)
		}
		if mi, err := p.MemoryInfo(); err == nil {
			row.RSS = mi.RSS
			if vm != nil && vm.Total > 0 {
				row.MemPct = round1(float64(mi.RSS) / float64(vm.Total) * 100)
			}
		}
		rows = append(rows, row)
	}
	procPrev, procAt = next, now

	sortBy := r.URL.Query().Get("sort")
	sort.Slice(rows, func(i, j int) bool {
		if sortBy == "mem" {
			return rows[i].RSS > rows[j].RSS
		}
		if rows[i].CPU == rows[j].CPU {
			return rows[i].RSS > rows[j].RSS
		}
		return rows[i].CPU > rows[j].CPU
	})
	if len(rows) > 25 {
		rows = rows[:25]
	}
	// only resolve names/users/cmdlines for the rows we return
	for i := range rows {
		p, err := process.NewProcess(rows[i].PID)
		if err != nil {
			continue
		}
		rows[i].Name, _ = p.Name()
		if uids, err := p.Uids(); err == nil && len(uids) > 0 {
			rows[i].User = lookupUser(int32(uids[0]))
		}
		if c, err := p.Cmdline(); err == nil {
			if len(c) > 200 {
				c = c[:200]
			}
			rows[i].Cmd = c
		}
	}
	writeJSON(w, map[string]any{"processes": rows, "total": len(procs)})
}

func handleKill(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PID    int    `json:"pid"`
		Signal string `json:"signal"`
	}
	if err := readJSON(r, &req); err != nil || req.PID <= 1 {
		httpErr(w, 400, "bad request")
		return
	}
	sig := syscall.SIGTERM
	if req.Signal == "KILL" {
		sig = syscall.SIGKILL
	}
	if err := syscall.Kill(req.PID, sig); err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// ---------- listening ports ----------

func handlePorts(w http.ResponseWriter, r *http.Request) {
	conns, err := gnet.Connections("inet")
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	type row struct {
		Proto string `json:"proto"`
		Addr  string `json:"addr"`
		Port  uint32 `json:"port"`
		PID   int32  `json:"pid"`
		Name  string `json:"name"`
	}
	seen := map[string]bool{}
	names := map[int32]string{}
	var out []row
	for _, c := range conns {
		isUDP := c.Type == syscall.SOCK_DGRAM
		if !isUDP && c.Status != "LISTEN" {
			continue
		}
		if isUDP && c.Raddr.Port != 0 {
			continue
		}
		proto := "tcp"
		if isUDP {
			proto = "udp"
		}
		key := proto + c.Laddr.IP + ":" + strconv.Itoa(int(c.Laddr.Port))
		if seen[key] {
			continue
		}
		seen[key] = true
		n, ok := names[c.Pid]
		if !ok && c.Pid > 0 {
			if p, err := process.NewProcess(c.Pid); err == nil {
				n, _ = p.Name()
			}
			names[c.Pid] = n
		}
		out = append(out, row{Proto: proto, Addr: c.Laddr.IP, Port: c.Laddr.Port, PID: c.Pid, Name: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port == out[j].Port {
			return out[i].Proto < out[j].Proto
		}
		return out[i].Port < out[j].Port
	})
	writeJSON(w, map[string]any{"ports": out})
}
