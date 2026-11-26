package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

//go:embed index.html
var indexHTML []byte

//go:embed assets/xterm/*
var xtermAssets embed.FS

const (
	defaultAddr            = "127.0.0.1:8791"
	defaultInterval        = 60 * time.Second
	defaultSSHTimeout      = 5 * time.Second
	defaultSSHMac          = "mac-tailscale"
	defaultSSHTokyo        = "ssh -p 25528 root@jp-tyo01.tryaltare.ai"
	defaultSSHInstabox     = "instabox-vps-1-ts"
	defaultTermIdleTimeout = 30 * time.Minute
	defaultTermMaxSessions = 6
	authCookieName         = "tailmon_auth"
	termPinCookieName      = "tailmon_term_auth"
)

type Config struct {
	Addr            string
	Password        string
	TerminalPin     string
	Interval        time.Duration
	SSHTimeout      time.Duration
	TermIdleTimeout time.Duration
	TermMaxSessions int
	SSHMac          string
	SSHTokyo        string
	SSHVPS          string
	SSHInstabox     string
	SSHTargets      map[string]string
	TailscaleBin    string
}

type DiskMount struct {
	Target string `json:"target"`
	Total  uint64 `json:"total"`
	Used   uint64 `json:"used"`
}

type Device struct {
	Hostname      string      `json:"hostname"`
	DNSName       string      `json:"dns_name"`
	OS            string      `json:"os"`
	OSVersion     string      `json:"os_version"`
	TailscaleIP   string      `json:"tailscale_ip"`
	Online        bool        `json:"online"`
	Active        bool        `json:"active"`
	IsSelf        bool        `json:"is_self"`
	Relay         string      `json:"relay"`
	LastSeen      string      `json:"last_seen"`
	KeyExpiry     string      `json:"key_expiry"`
	TxBytes       int64       `json:"tx_bytes"`
	RxBytes       int64       `json:"rx_bytes"`
	SpecStatus    string      `json:"spec_status"`
	SpecNote      string      `json:"spec_note,omitempty"`
	CPUModel      string      `json:"cpu_model,omitempty"`
	CPUCores      int         `json:"cpu_cores,omitempty"`
	LoadAvg       string      `json:"load_avg,omitempty"`
	MemTotal      uint64      `json:"mem_total,omitempty"`
	MemUsed       uint64      `json:"mem_used,omitempty"`
	Disk              []DiskMount `json:"disk,omitempty"`
	UptimeSeconds     int64       `json:"uptime_seconds,omitempty"`
	TerminalAvailable bool        `json:"terminal_available"`
	TerminalTargetID  string      `json:"terminal_target_id,omitempty"`
}

type Snapshot struct {
	CollectedAt         string   `json:"collected_at"`
	Stale               bool     `json:"stale"`
	Error               string   `json:"error,omitempty"`
	Tailnet             string   `json:"tailnet,omitempty"`
	SelfHostname        string   `json:"self_hostname,omitempty"`
	AuthEnabled         bool     `json:"auth_enabled"`
	TerminalEnabled     bool     `json:"terminal_enabled"`
	TerminalPinRequired bool     `json:"terminal_pin_required"`
	Devices             []Device `json:"devices"`
}

// tailscaleStatus mirrors the relevant fields of `tailscale status --json`.
type tailscaleStatus struct {
	CurrentTailnet *struct {
		Name string `json:"Name"`
	} `json:"CurrentTailnet"`
	Self *tailscalePeer            `json:"Self"`
	Peer map[string]*tailscalePeer `json:"Peer"`
}

type tailscalePeer struct {
	HostName     string   `json:"HostName"`
	DNSName      string   `json:"DNSName"`
	OS           string   `json:"OS"`
	TailscaleIPs []string `json:"TailscaleIPs"`
	Online       bool     `json:"Online"`
	Active       bool     `json:"Active"`
	Relay        string   `json:"Relay"`
	LastSeen     string   `json:"LastSeen"`
	KeyExpiry    string   `json:"KeyExpiry"`
	TxBytes      int64    `json:"TxBytes"`
	RxBytes      int64    `json:"RxBytes"`
}

type nodeSpec struct {
	OSVersion     string
	CPUModel      string
	CPUCores      int
	LoadAvg       string
	MemTotal      uint64
	MemUsed       uint64
	Disk          []DiskMount
	UptimeSeconds int64
}

type Collector struct {
	cfg Config

	mu       sync.RWMutex
	snapshot Snapshot

	readyOnce sync.Once
	ready     chan struct{}

	refreshing atomic.Bool
}

func NewCollector(cfg Config) *Collector {
	return &Collector{
		cfg:   cfg,
		ready: make(chan struct{}),
		snapshot: Snapshot{
			AuthEnabled:         cfg.Password != "",
			TerminalEnabled:     cfg.Password != "",
			TerminalPinRequired: cfg.Password != "" && cfg.TerminalPin != "",
			Devices:             []Device{},
		},
	}
}

func (c *Collector) markReady() {
	c.readyOnce.Do(func() {
		close(c.ready)
	})
}

func (c *Collector) WaitReady(ctx context.Context) {
	select {
	case <-c.ready:
	case <-ctx.Done():
	}
}

func (c *Collector) GetSnapshot() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()

	snap := c.snapshot
	if c.snapshot.Devices != nil {
		snap.Devices = make([]Device, len(c.snapshot.Devices))
		for i, d := range c.snapshot.Devices {
			if len(d.Disk) > 0 {
				disks := make([]DiskMount, len(d.Disk))
				copy(disks, d.Disk)
				d.Disk = disks
			}
			snap.Devices[i] = d
		}
	} else {
		snap.Devices = []Device{}
	}
	return snap
}

func (c *Collector) Refresh(ctx context.Context) {
	if !c.refreshing.CompareAndSwap(false, true) {
		return
	}
	defer c.refreshing.Store(false)
	defer c.markReady()

	next, err := c.collectOnce(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()

	if err != nil {
		log.Printf("poll failed: %v", err)
		c.snapshot.Stale = true
		c.snapshot.Error = err.Error()
		c.snapshot.AuthEnabled = c.cfg.Password != ""
		c.snapshot.TerminalEnabled = c.cfg.Password != ""
		c.snapshot.TerminalPinRequired = c.cfg.Password != "" && c.cfg.TerminalPin != ""
		if c.snapshot.Devices == nil {
			c.snapshot.Devices = []Device{}
		}
		return
	}

	c.snapshot = next
}

func (c *Collector) Run(ctx context.Context) {
	c.Refresh(ctx)
	ticker := time.NewTicker(c.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.Refresh(ctx)
		}
	}
}

func (c *Collector) collectOnce(ctx context.Context) (Snapshot, error) {
	tsCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(tsCtx, c.cfg.TailscaleBin, "status", "--json")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return Snapshot{}, fmt.Errorf("tailscale status: %w (%s)", err, errMsg)
		}
		return Snapshot{}, fmt.Errorf("tailscale status: %w", err)
	}

	var status tailscaleStatus
	if err := json.Unmarshal(stdout.Bytes(), &status); err != nil {
		return Snapshot{}, fmt.Errorf("parse tailscale status: %w", err)
	}

	devices := make([]Device, 0, 1+len(status.Peer))
	var selfHostname string
	if status.Self != nil {
		d := peerToDevice(status.Self, true)
		selfHostname = d.Hostname
		devices = append(devices, d)
	}
	for _, peer := range status.Peer {
		if peer == nil {
			continue
		}
		devices = append(devices, peerToDevice(peer, false))
	}

	var wg sync.WaitGroup
	for i := range devices {
		i := i
		target, osKind, note, probe := c.resolveProbeTarget(devices[i])
		if !probe {
			continue
		}
		if devices[i].IsSelf || devices[i].Online {
			devices[i].TerminalAvailable = true
			devices[i].TerminalTargetID = canonicalDeviceID(devices[i])
		}

		wg.Add(1)
		go func() {
			defer wg.Done()

			probeCtx, probeCancel := context.WithTimeout(ctx, c.cfg.SSHTimeout)
			defer probeCancel()

			var spec nodeSpec
			var err error
			if devices[i].IsSelf {
				spec, err = runLocalProbe(probeCtx, osKind)
			} else {
				spec, err = runSSHProbe(probeCtx, target, osKind)
			}
			if err != nil {
				log.Printf("spec probe failed for %s (%s): %v", devices[i].Hostname, devices[i].TailscaleIP, err)
				devices[i].SpecStatus = "unavailable"
				return
			}

			applySpec(&devices[i], spec, note)
		}()
	}
	wg.Wait()

	sortDevices(devices)

	var tailnet string
	if status.CurrentTailnet != nil {
		tailnet = status.CurrentTailnet.Name
	}

	return Snapshot{
		CollectedAt:         time.Now().UTC().Format(time.RFC3339),
		Stale:               false,
		Tailnet:             tailnet,
		SelfHostname:        selfHostname,
		AuthEnabled:         c.cfg.Password != "",
		TerminalEnabled:     c.cfg.Password != "",
		TerminalPinRequired: c.cfg.Password != "" && c.cfg.TerminalPin != "",
		Devices:             devices,
	}, nil
}

func peerToDevice(p *tailscalePeer, isSelf bool) Device {
	hostname := strings.TrimSpace(p.HostName)
	dnsName := strings.TrimSuffix(strings.TrimSpace(p.DNSName), ".")

	var ip string
	for _, candidate := range p.TailscaleIPs {
		if !strings.Contains(candidate, ":") {
			ip = candidate
			break
		}
	}
	if ip == "" && len(p.TailscaleIPs) > 0 {
		ip = p.TailscaleIPs[0]
	}

	lastSeen := ""
	if !p.Online {
		lastSeen = formatValidTimestamp(p.LastSeen)
	}

	return Device{
		Hostname:    hostname,
		DNSName:     dnsName,
		OS:          strings.TrimSpace(p.OS),
		OSVersion:   "",
		TailscaleIP: ip,
		Online:      p.Online,
		Active:      p.Active,
		IsSelf:      isSelf,
		Relay:       strings.TrimSpace(p.Relay),
		LastSeen:    lastSeen,
		KeyExpiry:   formatValidTimestamp(p.KeyExpiry),
		TxBytes:     p.TxBytes,
		RxBytes:     p.RxBytes,
		SpecStatus:  "n/a",
	}
}

func formatValidTimestamp(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			return ""
		}
	}
	if t.IsZero() || t.Year() <= 1 {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func canonicalDeviceID(d Device) string {
	for _, cand := range []string{d.TailscaleIP, d.DNSName, d.Hostname} {
		if s := strings.ToLower(strings.TrimSpace(cand)); s != "" {
			return s
		}
	}
	return ""
}

func osKindFromDevice(d Device) string {
	osLower := strings.ToLower(strings.TrimSpace(d.OS))
	if osLower == "macos" || osLower == "darwin" {
		return "darwin"
	}
	return "linux"
}

func parseSSHTargets(raw string) map[string]string {
	out := make(map[string]string)
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		k, v, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		if k == "" || v == "" {
			continue
		}
		out[k] = v
	}
	return out
}

func (c *Collector) resolveProbeTarget(d Device) (sshTarget string, osKind string, specNote string, shouldProbe bool) {
	if d.IsSelf {
		return "", runtime.GOOS, "", true
	}
	if !d.Online {
		return "", "", "", false
	}

	dnsLower := strings.ToLower(strings.TrimSpace(d.DNSName))
	shortDNS := dnsLower
	if idx := strings.IndexByte(shortDNS, '.'); idx > 0 {
		shortDNS = shortDNS[:idx]
	}
	hostLower := strings.ToLower(strings.TrimSpace(d.Hostname))
	ipLower := strings.ToLower(strings.TrimSpace(d.TailscaleIP))
	osLower := strings.ToLower(strings.TrimSpace(d.OS))

	for _, key := range []string{hostLower, shortDNS, dnsLower, ipLower} {
		if key != "" {
			if target, ok := c.cfg.SSHTargets[key]; ok && target != "" {
				return target, osKindFromDevice(d), "", true
			}
		}
	}

	if strings.HasPrefix(dnsLower, "adityas-macbook-pro") || osLower == "macos" || osLower == "darwin" {
		if c.cfg.SSHMac != "" {
			return c.cfg.SSHMac, "darwin", "", true
		}
		return "", "", "", false
	}

	if strings.HasPrefix(dnsLower, "tokyo-ubuntu") || strings.Contains(hostLower, "tokyo") {
		if c.cfg.SSHTokyo != "" {
			return c.cfg.SSHTokyo, "linux", "host values", true
		}
		return "", "", "", false
	}

	if strings.HasPrefix(dnsLower, "instabox-vps-1") || strings.Contains(hostLower, "instabox") {
		if c.cfg.SSHInstabox != "" {
			return c.cfg.SSHInstabox, "linux", "", true
		}
		return "", "", "", false
	}

	if strings.HasPrefix(dnsLower, "vm-0-16-ubuntu") && c.cfg.SSHVPS != "" {
		return c.cfg.SSHVPS, "linux", "", true
	}

	return "", "", "", false
}

func applySpec(d *Device, spec nodeSpec, note string) {
	if spec.OSVersion == "" && spec.CPUCores == 0 && spec.MemTotal == 0 && len(spec.Disk) == 0 {
		d.SpecStatus = "unavailable"
		return
	}
	d.SpecStatus = "ok"
	d.SpecNote = note
	d.OSVersion = spec.OSVersion
	d.CPUModel = spec.CPUModel
	d.CPUCores = spec.CPUCores
	d.LoadAvg = spec.LoadAvg
	d.MemTotal = spec.MemTotal
	d.MemUsed = spec.MemUsed
	d.Disk = spec.Disk
	d.UptimeSeconds = spec.UptimeSeconds
}

func sortDevices(devices []Device) {
	sort.SliceStable(devices, func(i, j int) bool {
		a, b := devices[i], devices[j]
		if a.Online != b.Online {
			return a.Online
		}
		if a.Online {
			if a.IsSelf != b.IsSelf {
				return a.IsSelf
			}
			if a.Active != b.Active {
				return a.Active
			}
		} else {
			if a.LastSeen != b.LastSeen {
				if a.LastSeen == "" {
					return false
				}
				if b.LastSeen == "" {
					return true
				}
				return a.LastSeen > b.LastSeen
			}
		}
		ha, hb := strings.ToLower(a.Hostname), strings.ToLower(b.Hostname)
		if ha != hb {
			return ha < hb
		}
		return strings.ToLower(a.DNSName) < strings.ToLower(b.DNSName)
	})
}

const darwinProbeScript = `
sw_name=$(sw_vers -productName 2>/dev/null || echo "macOS")
sw_ver=$(sw_vers -productVersion 2>/dev/null || echo "")
if [ -n "$sw_ver" ]; then
  printf 'OS_VER=%s %s\n' "$sw_name" "$sw_ver"
else
  printf 'OS_VER=%s\n' "$sw_name"
fi
printf 'CPU_MODEL=%s\n' "$(sysctl -n machdep.cpu.brand_string 2>/dev/null)"
printf 'CPU_CORES=%s\n' "$(sysctl -n hw.ncpu 2>/dev/null)"
la=$(sysctl -n vm.loadavg 2>/dev/null | tr -d '{}' | awk '{print $1, $2, $3}')
printf 'LOAD_AVG=%s\n' "$la"
boot=$(sysctl -n kern.boottime 2>/dev/null | awk '{for (i = 1; i <= NF; i++) if ($i == "sec") { gsub(/,/, "", $(i + 2)); print $(i + 2); exit }}')
now=$(date +%s)
if [ -n "$boot" ]; then
  printf 'UPTIME=%s\n' "$((now - boot))"
fi
mem_total=$(sysctl -n hw.memsize 2>/dev/null)
printf 'MEM_TOTAL=%s\n' "$mem_total"
vm_stat 2>/dev/null | awk -v total="$mem_total" '
/page size of [0-9]+ bytes/ {
  for (i = 1; i <= NF; i++) {
    if ($i == "of") {
      ps = $(i + 1) + 0
      break
    }
  }
}
{
  val = $NF
  gsub(/\./, "", val)
}
/^Anonymous pages:/ { anon = val + 0 }
/^Pages purgeable:/ { purg = val + 0 }
/^Pages wired down:/ { wired = val + 0 }
/^Pages active:/ { active = val + 0 }
/^Pages occupied by compressor:/ { comp = val + 0 }
END {
  if (ps > 0) {
    if (anon > 0) used = (anon - purg + wired + comp) * ps
    else used = (active + wired + comp) * ps
    if (used < 0) used = 0
    if (total > 0 && used > total) used = total
    printf "MEM_USED=%.0f\n", used
  }
}'
if df -k /System/Volumes/Data >/dev/null 2>&1; then
  df -k /System/Volumes/Data | awk 'NR==2 {total=$2*1024; used=($2-$4)*1024; if (used<0) used=0; printf "DISK=/|%.0f|%.0f\n", total, used}'
else
  df -k / | awk 'NR==2 {total=$2*1024; used=$3*1024; printf "DISK=/|%.0f|%.0f\n", total, used}'
fi
`

const linuxProbeScript = `
os_ver=""
if [ -r /etc/os-release ]; then
  os_ver=$(awk -F= '
    /^PRETTY_NAME=/ { gsub(/^"|"$/, "", $2); p=$2 }
    /^NAME=/ { gsub(/^"|"$/, "", $2); n=$2 }
    /^VERSION=/ { gsub(/^"|"$/, "", $2); v=$2 }
    END { if (p != "") print p; else if (n != "") print n (v != "" ? " " v : "") }
  ' /etc/os-release)
fi
if [ -z "$os_ver" ]; then
  os_ver=$(uname -sr 2>/dev/null)
fi
printf 'OS_VER=%s\n' "$os_ver"

cpu_model=$(awk -F: '
  /^[Mm]odel name[[:space:]]*:/ && m=="" { gsub(/^[[:space:]]+/, "", $2); m=$2 }
  /^[Mm]odel[[:space:]]*:/ && o=="" { gsub(/^[[:space:]]+/, "", $2); o=$2 }
  /^[Hh]ardware[[:space:]]*:/ && h=="" { gsub(/^[[:space:]]+/, "", $2); h=$2 }
  END { if (m != "") print m; else if (o != "") print o; else if (h != "") print h }
' /proc/cpuinfo 2>/dev/null)
if [ -z "$cpu_model" ]; then
  cpu_model=$(uname -m 2>/dev/null)
fi
printf 'CPU_MODEL=%s\n' "$cpu_model"

cores=$(nproc 2>/dev/null || grep -c '^processor' /proc/cpuinfo 2>/dev/null)
printf 'CPU_CORES=%s\n' "$cores"

if [ -r /proc/loadavg ]; then
  printf 'LOAD_AVG=%s\n' "$(awk '{print $1, $2, $3}' /proc/loadavg)"
fi

if [ -r /proc/uptime ]; then
  printf 'UPTIME=%s\n' "$(awk '{printf "%d", $1}' /proc/uptime)"
fi

if [ -r /proc/meminfo ]; then
  awk '
    /^MemTotal:/ { total = $2 * 1024 }
    /^MemAvailable:/ { avail = $2 * 1024; has_avail = 1 }
    /^MemFree:/ { mfree = $2 * 1024 }
    /^Buffers:/ { bufs = $2 * 1024 }
    /^Cached:/ { cached = $2 * 1024 }
    END {
      if (total > 0) {
        if (has_avail) used = total - avail
        else used = total - mfree - bufs - cached
        if (used < 0) used = 0
        if (used > total) used = total
        printf "MEM_TOTAL=%.0f\nMEM_USED=%.0f\n", total, used
      }
    }
  ' /proc/meminfo
fi

root_line=$(df -P -B1 / 2>/dev/null | awk 'NR==2 {printf "DISK=%s|%.0f|%.0f", $6, $2, $3}')
if [ -z "$root_line" ]; then
  root_line=$(df -Pk / 2>/dev/null | awk 'NR==2 {printf "DISK=%s|%.0f|%.0f", $6, $2*1024, $3*1024}')
fi
if [ -n "$root_line" ]; then
  printf '%s\n' "$root_line"
fi

df -P -B1 -x tmpfs -x devtmpfs -x squashfs -x overlay -x efivarfs 2>/dev/null | awk '
  NR>1 && $6 != "/" && $6 !~ /^\/(boot|snap|run|sys|proc|dev)($|\/)/ && $2+0 > 0 {
    if (!seen[$6]++) printf "DISK=%s|%.0f|%.0f\n", $6, $2, $3
  }
'
`

func runLocalProbe(ctx context.Context, osKind string) (nodeSpec, error) {
	script := linuxProbeScript
	if osKind == "darwin" {
		script = darwinProbeScript
	}
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nodeSpec{}, fmt.Errorf("local probe: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	return parseProbeOutput(stdout.String()), nil
}

func runSSHProbe(ctx context.Context, target string, osKind string) (nodeSpec, error) {
	fields := strings.Fields(target)
	if len(fields) == 0 {
		return nodeSpec{}, errors.New("empty ssh target")
	}
	if fields[0] == "ssh" {
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return nodeSpec{}, errors.New("missing ssh destination")
	}

	script := linuxProbeScript
	if osKind == "darwin" {
		script = darwinProbeScript
	}

	args := []string{
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=4",
		"-o", "ServerAliveInterval=2",
		"-o", "ServerAliveCountMax=1",
	}
	args = append(args, fields...)
	args = append(args, script)

	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = nil
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return nodeSpec{}, fmt.Errorf("ssh probe: %w (%s)", err, errMsg)
		}
		return nodeSpec{}, fmt.Errorf("ssh probe: %w", err)
	}

	return parseProbeOutput(stdout.String()), nil
}

func parseProbeOutput(out string) nodeSpec {
	var spec nodeSpec
	seenMounts := make(map[string]bool)

	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch k {
		case "OS_VER":
			spec.OSVersion = v
		case "CPU_MODEL":
			spec.CPUModel = v
		case "CPU_CORES":
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				spec.CPUCores = n
			}
		case "LOAD_AVG":
			spec.LoadAvg = v
		case "UPTIME":
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
				spec.UptimeSeconds = n
			}
		case "MEM_TOTAL":
			if n, err := strconv.ParseUint(v, 10, 64); err == nil && n > 0 {
				spec.MemTotal = n
			}
		case "MEM_USED":
			if n, err := strconv.ParseUint(v, 10, 64); err == nil {
				spec.MemUsed = n
			}
		case "DISK":
			parts := strings.Split(v, "|")
			if len(parts) != 3 {
				continue
			}
			target := strings.TrimSpace(parts[0])
			total, err1 := strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 64)
			used, err2 := strconv.ParseUint(strings.TrimSpace(parts[2]), 10, 64)
			if err1 != nil || err2 != nil || target == "" || total == 0 || seenMounts[target] {
				continue
			}
			seenMounts[target] = true
			spec.Disk = append(spec.Disk, DiskMount{
				Target: target,
				Total:  total,
				Used:   used,
			})
		}
	}
	return spec
}

func computeAuthToken(password string) string {
	mac := hmac.New(sha256.New, []byte(password))
	mac.Write([]byte("tailmon-session-v1"))
	return hex.EncodeToString(mac.Sum(nil))
}

func constantTimeEqual(a, b string) bool {
	if len(a) == 0 || len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func isAuthenticated(r *http.Request, password, cookieToken string) bool {
	if password == "" {
		return true
	}
	authz := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(authz) > 7 && strings.EqualFold(authz[:7], "Bearer ") {
		token := strings.TrimSpace(authz[7:])
		if constantTimeEqual(token, password) {
			return true
		}
	}
	if cookie, err := r.Cookie(authCookieName); err == nil {
		if constantTimeEqual(cookie.Value, cookieToken) {
			return true
		}
	}
	return false
}

func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func renderLoginPage(w http.ResponseWriter, status int, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)

	errBlock := ""
	if errMsg != "" {
		errBlock = fmt.Sprintf(`<div class="err" role="alert"><span class="err-dot" aria-hidden="true">!</span><span>%s</span></div>`, html.EscapeString(errMsg))
	}

	const loginTemplate = `<!DOCTYPE html>
<html lang="en" data-theme="light">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Tailscale Monitor — Sign In</title>
<link rel="icon" type="image/svg+xml" href="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24'%3E%3Crect width='24' height='24' rx='6' fill='%2318181F'/%3E%3Crect x='10.2' y='2.6' width='3.6' height='3.6' rx='1' fill='%23fff'/%3E%3Crect x='10.2' y='17.8' width='3.6' height='3.6' rx='1' fill='%23fff'/%3E%3Crect x='2.6' y='10.2' width='3.6' height='3.6' rx='1' fill='%23fff'/%3E%3Crect x='17.8' y='10.2' width='3.6' height='3.6' rx='1' fill='%23fff'/%3E%3Crect x='5' y='5' width='3.2' height='3.2' rx='1' fill='%23fff' fill-opacity='.88'/%3E%3Crect x='15.8' y='5' width='3.2' height='3.2' rx='1' fill='%23fff' fill-opacity='.88'/%3E%3Crect x='5' y='15.8' width='3.2' height='3.2' rx='1' fill='%23fff' fill-opacity='.88'/%3E%3Crect x='15.8' y='15.8' width='3.2' height='3.2' rx='1' fill='%23fff' fill-opacity='.88'/%3E%3C/svg%3E">
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600&family=JetBrains+Mono:wght@400;500&display=swap" rel="stylesheet">
<script>
(function () {
  var saved = 'light';
  try { saved = localStorage.getItem('tailmon_theme') || 'light'; } catch (_) {}
  if (saved !== 'light' && saved !== 'dark' && saved !== 'auto') saved = 'light';
  var resolved = saved === 'auto'
    ? (window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light')
    : saved;
  document.documentElement.dataset.theme = resolved;
})();
</script>
<style>
  :root {
    --font-sans: "Inter", -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
    --page-bg: #FFFFFF;
    --shell-bg: #F6F6F8;
    --shell-border: #EBEBF0;
    --shell-shadow: 0 22px 54px -24px rgba(15, 23, 42, 0.16);
    --shell-rim: linear-gradient(90deg, transparent 0%, #FFFFFF 50%, transparent 100%);
    --surface: #FFFFFF;
    --surface-inset: #F4F4F8;
    --border: #EBEBF0;
    --border-strong: #DCDCE4;
    --text: #18181F;
    --muted: #6E6E7A;
    --mesh: rgba(130, 95, 175, 0.09);
    --wash: linear-gradient(180deg, rgba(247, 241, 252, 0.85) 0%, rgba(237, 246, 254, 0.45) 55%, transparent 100%);
    --hub-ring-bg: rgba(255, 255, 255, 0.96);
    --hub-ring-border: #FFFFFF;
    --hub-ring-shadow: 0 14px 28px -8px rgba(15, 23, 42, 0.22);
    --ring-focus: rgba(24, 24, 31, 0.14);
    --danger: #E86A42;
    --danger-bg: rgba(232, 106, 66, 0.12);
    --danger-border: rgba(232, 106, 66, 0.34);
    --danger-text: #B91C1C;
  }
  html[data-theme="dark"] {
    --page-bg: #09090C;
    --shell-bg: #101014;
    --shell-border: rgba(39, 39, 42, 0.92);
    --shell-shadow: 0 24px 60px -24px rgba(0, 0, 0, 0.72);
    --shell-rim: linear-gradient(90deg, transparent 0%, rgba(255, 255, 255, 0.15) 50%, transparent 100%);
    --surface: rgba(24, 24, 27, 0.96);
    --surface-inset: #131318;
    --border: rgba(39, 39, 42, 0.92);
    --border-strong: #2E2E36;
    --text: #F4F4F5;
    --muted: #A1A1AA;
    --mesh: rgba(175, 138, 226, 0.08);
    --wash: linear-gradient(180deg, rgba(35, 27, 46, 0.7) 0%, rgba(22, 34, 51, 0.35) 55%, transparent 100%);
    --hub-ring-bg: rgba(39, 39, 42, 0.85);
    --hub-ring-border: rgba(63, 63, 70, 0.85);
    --hub-ring-shadow: 0 14px 28px -8px rgba(0, 0, 0, 0.72);
    --ring-focus: rgba(255, 255, 255, 0.15);
    --danger-bg: rgba(232, 106, 66, 0.15);
    --danger-border: rgba(232, 106, 66, 0.36);
    --danger-text: #FCA5A5;
  }
  * { box-sizing: border-box; }
  body {
    margin: 0;
    min-height: 100vh;
    display: grid;
    place-items: center;
    padding: 20px;
    background: var(--page-bg);
    color: var(--text);
    font-family: var(--font-sans);
    -webkit-font-smoothing: antialiased;
  }
  .shell {
    position: relative;
    width: 100%;
    max-width: 420px;
    overflow: hidden;
    border-radius: 28px;
    border: 1px solid var(--shell-border);
    background: var(--shell-bg);
    box-shadow: var(--shell-shadow);
    padding: 32px 26px 26px;
  }
  .shell::before {
    content: "";
    position: absolute;
    inset: 0 0 auto 0;
    height: 1px;
    background: var(--shell-rim);
    pointer-events: none;
    z-index: 3;
  }
  .wash {
    position: absolute;
    inset: 0 0 auto 0;
    height: 190px;
    background: var(--wash);
    pointer-events: none;
    z-index: 0;
  }
  .mesh {
    position: absolute;
    inset: 0 0 auto 0;
    height: 190px;
    background-image:
      linear-gradient(to right, var(--mesh) 1px, transparent 1px),
      linear-gradient(to bottom, var(--mesh) 1px, transparent 1px);
    background-size: 5px 5px;
    -webkit-mask-image: linear-gradient(to bottom, rgba(0,0,0,0.85) 0%, transparent 100%);
    mask-image: linear-gradient(to bottom, rgba(0,0,0,0.85) 0%, transparent 100%);
    pointer-events: none;
    z-index: 1;
  }
  .content {
    position: relative;
    z-index: 2;
  }
  .head {
    display: flex;
    flex-direction: column;
    align-items: center;
    text-align: center;
  }
  .eyebrow {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 4px 10px;
    border-radius: 999px;
    border: 1px solid var(--border);
    background: var(--surface);
    color: var(--muted);
    font-size: 10px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    box-shadow: 0 2px 8px rgba(15, 23, 42, 0.05);
  }
  .eyebrow-dot-outer {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 12px;
    height: 12px;
    border-radius: 50%;
    background: var(--surface-inset);
  }
  .eyebrow-dot-inner {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--text);
  }
  .hub-ring {
    margin-top: 18px;
    border-radius: 999px;
    padding: 5px;
    background: var(--hub-ring-bg);
    border: 1px solid var(--hub-ring-border);
    box-shadow: var(--hub-ring-shadow);
  }
  .hub-pill {
    display: inline-flex;
    align-items: center;
    gap: 9px;
    padding: 9px 18px;
    border-radius: 999px;
    background: linear-gradient(180deg, #2B2B32 0%, #1B1B20 52%, #121216 100%);
    color: #FFFFFF;
    font-size: 15px;
    font-weight: 500;
    letter-spacing: -0.025em;
    box-shadow: inset 0 1px 1px rgba(255,255,255,0.24), inset 0 -2px 4px rgba(0,0,0,0.5);
  }
  h1 {
    margin: 18px 0 6px;
    font-size: 22px;
    font-weight: 500;
    letter-spacing: -0.03em;
  }
  p {
    margin: 0 0 22px;
    font-size: 13px;
    line-height: 1.5;
    color: var(--muted);
  }
  .card {
    border-radius: 18px;
    border: 1px solid var(--border);
    background: var(--surface);
    padding: 20px;
    box-shadow: 0 8px 24px -10px rgba(15, 23, 42, 0.08);
  }
  label {
    display: block;
    font-size: 11.5px;
    font-weight: 500;
    color: var(--muted);
    margin-bottom: 7px;
  }
  input[type="password"] {
    width: 100%;
    height: 42px;
    padding: 0 13px;
    border-radius: 11px;
    border: 1px solid var(--border-strong);
    background: var(--surface-inset);
    color: var(--text);
    font-family: var(--font-sans);
    font-size: 14px;
    outline: none;
    transition: border-color 0.16s ease, box-shadow 0.16s ease;
  }
  input[type="password"]:focus {
    border-color: var(--text);
    box-shadow: 0 0 0 3px var(--ring-focus);
  }
  button[type="submit"] {
    width: 100%;
    height: 42px;
    margin-top: 14px;
    border: 0;
    border-radius: 999px;
    background: linear-gradient(180deg, #2B2B32 0%, #1B1B20 55%, #121216 100%);
    color: #FFFFFF;
    font-family: var(--font-sans);
    font-weight: 500;
    font-size: 13.5px;
    letter-spacing: -0.01em;
    cursor: pointer;
    box-shadow: inset 0 1px 1px rgba(255,255,255,0.22), 0 8px 18px -8px rgba(15,23,42,0.45);
    transition: transform 0.14s ease, filter 0.16s ease;
  }
  button[type="submit"]:hover { filter: brightness(1.08); }
  button[type="submit"]:active { transform: scale(0.98); }
  .err {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-bottom: 14px;
    padding: 9px 11px;
    border-radius: 10px;
    background: var(--danger-bg);
    border: 1px solid var(--danger-border);
    color: var(--danger-text);
    font-size: 12.5px;
  }
  .err-dot {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 15px;
    height: 15px;
    border-radius: 50%;
    background: var(--danger);
    color: #FFFFFF;
    font-size: 10px;
    font-weight: 700;
    flex-shrink: 0;
  }
</style>
</head>
<body>
  <div class="shell">
    <div class="wash" aria-hidden="true"></div>
    <div class="mesh" aria-hidden="true"></div>
    <div class="content">
      <div class="head">
        <div class="eyebrow">
          <span class="eyebrow-dot-outer" aria-hidden="true"><span class="eyebrow-dot-inner"></span></span>
          <span>Protected Tailnet</span>
        </div>
        <div class="hub-ring">
          <div class="hub-pill">
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" aria-hidden="true">
              <rect x="10.2" y="2.2" width="3.6" height="3.6" rx="1" fill="#FFFFFF"/>
              <rect x="10.2" y="18.2" width="3.6" height="3.6" rx="1" fill="#FFFFFF"/>
              <rect x="2.2" y="10.2" width="3.6" height="3.6" rx="1" fill="#FFFFFF"/>
              <rect x="18.2" y="10.2" width="3.6" height="3.6" rx="1" fill="#FFFFFF"/>
              <rect x="4.8" y="4.8" width="3.4" height="3.4" rx="1" fill="#FFFFFF" fill-opacity="0.9"/>
              <rect x="15.8" y="4.8" width="3.4" height="3.4" rx="1" fill="#FFFFFF" fill-opacity="0.9"/>
              <rect x="4.8" y="15.8" width="3.4" height="3.4" rx="1" fill="#FFFFFF" fill-opacity="0.9"/>
              <rect x="15.8" y="15.8" width="3.4" height="3.4" rx="1" fill="#FFFFFF" fill-opacity="0.9"/>
            </svg>
            <span>tailmon</span>
          </div>
        </div>
        <h1>Tailscale Monitor</h1>
        <p>Enter the dashboard password to unlock live fleet telemetry.</p>
      </div>
      <form class="card" method="POST" action="/login">
        __ERR_BLOCK__
        <label for="password">Dashboard password</label>
        <input id="password" name="password" type="password" autocomplete="current-password" placeholder="Enter shared password" autofocus required>
        <button type="submit">Unlock dashboard</button>
      </form>
    </div>
  </div>
</body>
</html>`

	page := strings.Replace(loginTemplate, "__ERR_BLOCK__", errBlock, 1)
	_, _ = io.WriteString(w, page)
}

func resolveTailscaleBin(explicit string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err == nil {
			return explicit, nil
		}
		return "", fmt.Errorf("TAILMON_TAILSCALE_BIN %q not found", explicit)
	}
	if p, err := exec.LookPath("tailscale"); err == nil {
		return p, nil
	}
	candidates := []string{
		"/Applications/Tailscale.app/Contents/MacOS/Tailscale",
		"/opt/homebrew/bin/tailscale",
		"/usr/local/bin/tailscale",
		"/usr/bin/tailscale",
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c, nil
		}
	}
	return "", errors.New("tailscale binary not found in PATH or standard locations")
}

func envOrDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		log.Printf("invalid %s=%q, using %s", key, v, fallback)
		return fallback
	}
	return d
}

func envInt(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		log.Printf("invalid %s=%q, using %d", key, v, fallback)
		return fallback
	}
	return n
}

func main() {
	tsBin, err := resolveTailscaleBin(strings.TrimSpace(os.Getenv("TAILMON_TAILSCALE_BIN")))
	if err != nil {
		log.Fatalf("startup error: %v", err)
	}

	cfg := Config{
		Addr:            envOrDefault("TAILMON_ADDR", defaultAddr),
		Password:        os.Getenv("TAILMON_PASSWORD"),
		TerminalPin:     os.Getenv("TAILMON_TERMINAL_PIN"),
		Interval:        envDuration("TAILMON_INTERVAL", defaultInterval),
		SSHTimeout:      envDuration("TAILMON_SSH_TIMEOUT", defaultSSHTimeout),
		TermIdleTimeout: envDuration("TAILMON_TERM_IDLE_TIMEOUT", defaultTermIdleTimeout),
		TermMaxSessions: envInt("TAILMON_TERM_MAX_SESSIONS", defaultTermMaxSessions),
		SSHMac:          envOrDefault("TAILMON_SSH_MAC", defaultSSHMac),
		SSHTokyo:        envOrDefault("TAILMON_SSH_TOKYO", defaultSSHTokyo),
		SSHInstabox:     envOrDefault("TAILMON_SSH_INSTABOX", defaultSSHInstabox),
		SSHVPS:          strings.TrimSpace(os.Getenv("TAILMON_SSH_VPS")),
		SSHTargets:      parseSSHTargets(os.Getenv("TAILMON_SSH_TARGETS")),
		TailscaleBin:    tsBin,
	}

	var cookieToken string
	if cfg.Password != "" {
		cookieToken = computeAuthToken(cfg.Password)
		log.Printf("password gate enabled (Bearer token or /login cookie required)")
	} else {
		log.Printf("WARNING: TAILMON_PASSWORD is empty; dashboard is running without authentication (in-app terminal disabled)")
	}
	var pinCookieToken string
	if cfg.Password != "" && cfg.TerminalPin != "" {
		pinCookieToken = computeTerminalPinToken(cfg.TerminalPin)
		log.Printf("terminal secondary PIN gate enabled")
	}
	log.Printf("using tailscale binary: %s", cfg.TailscaleBin)

	collector := NewCollector(cfg)
	termManager := NewTerminalManager(cfg, collector, cookieToken, pinCookieToken)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go collector.Run(ctx)

	mux := http.NewServeMux()

	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if isAuthenticated(r, cfg.Password, cookieToken) {
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			}
			renderLoginPage(w, http.StatusOK, "")
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		isJSON := strings.Contains(r.Header.Get("Content-Type"), "application/json")
		var submitted string
		if isJSON {
			var body struct {
				Password string `json:"password"`
			}
			if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
				return
			}
			submitted = body.Password
		} else {
			if err := r.ParseForm(); err != nil {
				renderLoginPage(w, http.StatusBadRequest, "Invalid form submission.")
				return
			}
			submitted = r.FormValue("password")
		}

		if cfg.Password != "" && !constantTimeEqual(submitted, cfg.Password) {
			if isJSON {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid password"})
				return
			}
			renderLoginPage(w, http.StatusUnauthorized, "Invalid password.")
			return
		}

		if cfg.Password != "" {
			http.SetCookie(w, &http.Cookie{
				Name:     authCookieName,
				Value:    cookieToken,
				Path:     "/",
				MaxAge:   30 * 24 * 3600,
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
				Secure:   isHTTPS(r),
			})
		}

		if isJSON {
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})

	mux.HandleFunc("/logout", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     authCookieName,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			Secure:   isHTTPS(r),
		})
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})

	mux.HandleFunc("/api/devices.json", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		if !isAuthenticated(r, cfg.Password, cookieToken) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="tailmon"`)
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}

		waitCtx, cancel := context.WithTimeout(r.Context(), 7*time.Second)
		defer cancel()
		collector.WaitReady(waitCtx)

		writeJSON(w, http.StatusOK, collector.GetSnapshot())
	})

	mux.HandleFunc("/api/refresh", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		if !isAuthenticated(r, cfg.Password, cookieToken) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="tailmon"`)
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}

		refreshCtx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		collector.Refresh(refreshCtx)

		writeJSON(w, http.StatusOK, collector.GetSnapshot())
	})

	xtermSubFS, err := fs.Sub(xtermAssets, "assets/xterm")
	if err != nil {
		log.Fatalf("xterm embed error: %v", err)
	}
	xtermHandler := http.StripPrefix("/assets/xterm/", http.FileServer(http.FS(xtermSubFS)))
	mux.HandleFunc("/assets/xterm/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !isAuthenticated(r, cfg.Password, cookieToken) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="tailmon"`)
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=86400")
		xtermHandler.ServeHTTP(w, r)
	})

	mux.HandleFunc("/api/terminal/status", termManager.HandleStatus)
	mux.HandleFunc("/api/terminal/unlock", termManager.HandleUnlock)
	mux.HandleFunc("/api/terminal/ws", termManager.HandleWS)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			if !isAuthenticated(r, cfg.Password, cookieToken) {
				if strings.HasPrefix(r.URL.Path, "/api/") {
					w.Header().Set("WWW-Authenticate", `Bearer realm="tailmon"`)
					writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
					return
				}
				renderLoginPage(w, http.StatusUnauthorized, "")
				return
			}
			http.NotFound(w, r)
			return
		}

		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if !isAuthenticated(r, cfg.Password, cookieToken) {
			renderLoginPage(w, http.StatusUnauthorized, "")
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(indexHTML)
		}
	})

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("listening on http://%s", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		if err != nil {
			log.Fatalf("server error: %v", err)
		}
	case <-ctx.Done():
		log.Printf("shutting down...")
		termManager.CloseAll()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			log.Printf("shutdown error: %v", err)
		}
	}
}
