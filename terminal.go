package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty"
)

type TerminalTarget struct {
	DeviceID    string
	DisplayName string
	IsLocal     bool
	SSHFields   []string
}

type termControlMsg struct {
	Type string `json:"type"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

type activeTermSession struct {
	id     string
	device string
	cancel context.CancelFunc
	ptmx   *os.File
}

type TerminalManager struct {
	cfg            Config
	collector      *Collector
	cookieToken    string
	pinCookieToken string

	mu       sync.Mutex
	sessions map[string]*activeTermSession
	seq      uint64
}

func NewTerminalManager(cfg Config, collector *Collector, cookieToken, pinCookieToken string) *TerminalManager {
	return &TerminalManager{
		cfg:            cfg,
		collector:      collector,
		cookieToken:    cookieToken,
		pinCookieToken: pinCookieToken,
		sessions:       make(map[string]*activeTermSession),
	}
}

func computeTerminalPinToken(pin string) string {
	mac := hmac.New(sha256.New, []byte(pin))
	mac.Write([]byte("tailmon-term-pin-v1"))
	return hex.EncodeToString(mac.Sum(nil))
}

func isTerminalPinUnlocked(r *http.Request, pin, pinToken string) bool {
	if pin == "" {
		return true
	}
	cookie, err := r.Cookie(termPinCookieName)
	if err != nil {
		return false
	}
	return constantTimeEqual(cookie.Value, pinToken)
}

func (m *TerminalManager) ActiveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}

func (m *TerminalManager) CloseAll() {
	m.mu.Lock()
	list := make([]*activeTermSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		list = append(list, s)
	}
	m.mu.Unlock()

	for _, s := range list {
		s.cancel()
		if s.ptmx != nil {
			_ = s.ptmx.Close()
		}
	}
}

func (m *TerminalManager) acquireSlot(device string, cancel context.CancelFunc) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	maxSess := m.cfg.TermMaxSessions
	if maxSess <= 0 {
		maxSess = defaultTermMaxSessions
	}
	if len(m.sessions) >= maxSess {
		return "", false
	}
	m.seq++
	id := fmt.Sprintf("term-%d", m.seq)
	m.sessions[id] = &activeTermSession{
		id:     id,
		device: device,
		cancel: cancel,
	}
	return id, true
}

func (m *TerminalManager) attachPTY(id string, ptmx *os.File) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok {
		s.ptmx = ptmx
	}
}

func (m *TerminalManager) releaseSlot(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, id)
}

func (c *Collector) ResolveTerminalTarget(deviceID string) (TerminalTarget, error) {
	want := strings.ToLower(strings.TrimSpace(deviceID))
	if want == "" {
		return TerminalTarget{}, errors.New("missing device parameter")
	}

	snap := c.GetSnapshot()
	for _, d := range snap.Devices {
		id := canonicalDeviceID(d)
		hostLower := strings.ToLower(strings.TrimSpace(d.Hostname))
		dnsLower := strings.ToLower(strings.TrimSpace(d.DNSName))
		shortDNS := dnsLower
		if idx := strings.IndexByte(shortDNS, '.'); idx > 0 {
			shortDNS = shortDNS[:idx]
		}
		ipLower := strings.ToLower(strings.TrimSpace(d.TailscaleIP))

		if want != id && want != ipLower && want != dnsLower && want != hostLower && want != shortDNS {
			continue
		}

		if !d.Online && !d.IsSelf {
			return TerminalTarget{}, fmt.Errorf("device %q is offline", d.Hostname)
		}

		display := d.Hostname
		if display == "" {
			display = shortDNS
		}
		if display == "" {
			display = d.TailscaleIP
		}

		if d.IsSelf {
			return TerminalTarget{
				DeviceID:    id,
				DisplayName: display,
				IsLocal:     true,
			}, nil
		}

		sshTarget, _, _, allowed := c.resolveProbeTarget(d)
		if !allowed || strings.TrimSpace(sshTarget) == "" {
			return TerminalTarget{}, fmt.Errorf("terminal not configured for device %q", display)
		}

		fields := strings.Fields(sshTarget)
		if len(fields) > 0 && fields[0] == "ssh" {
			fields = fields[1:]
		}
		if len(fields) == 0 {
			return TerminalTarget{}, fmt.Errorf("invalid ssh target for device %q", display)
		}

		return TerminalTarget{
			DeviceID:    id,
			DisplayName: display,
			IsLocal:     false,
			SSHFields:   fields,
		}, nil
	}

	return TerminalTarget{}, fmt.Errorf("device %q not found", deviceID)
}

func clampTermDim(raw string, minVal, maxVal, fallback uint16) uint16 {
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return fallback
	}
	if v < int(minVal) {
		return minVal
	}
	if v > int(maxVal) {
		return maxVal
	}
	return uint16(v)
}

func resolveLocalShell() string {
	if sh := strings.TrimSpace(os.Getenv("SHELL")); sh != "" {
		if _, err := os.Stat(sh); err == nil {
			return sh
		}
	}
	for _, candidate := range []string{"/bin/bash", "/bin/zsh", "/bin/sh"} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "/bin/sh"
}

func (m *TerminalManager) HandleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if !isAuthenticated(r, m.cfg.Password, m.cookieToken) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="tailmon"`)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	enabled := m.cfg.Password != ""
	pinReq := enabled && m.cfg.TerminalPin != ""
	pinUnlocked := enabled && (!pinReq || isTerminalPinUnlocked(r, m.cfg.TerminalPin, m.pinCookieToken))
	maxSess := m.cfg.TermMaxSessions
	if maxSess <= 0 {
		maxSess = defaultTermMaxSessions
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":         enabled,
		"pin_required":    pinReq,
		"pin_unlocked":    pinUnlocked,
		"active_sessions": m.ActiveCount(),
		"max_sessions":    maxSess,
	})
}

func (m *TerminalManager) HandleUnlock(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if !isAuthenticated(r, m.cfg.Password, m.cookieToken) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="tailmon"`)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if m.cfg.Password == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "terminal disabled: TAILMON_PASSWORD must be set"})
		return
	}
	if m.cfg.TerminalPin == "" {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}

	var body struct {
		Pin string `json:"pin"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if !constantTimeEqual(body.Pin, m.cfg.TerminalPin) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid terminal pin"})
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     termPinCookieName,
		Value:    m.pinCookieToken,
		Path:     "/",
		MaxAge:   12 * 3600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isHTTPS(r),
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (m *TerminalManager) HandleWS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if m.cfg.Password == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "terminal disabled: TAILMON_PASSWORD must be set"})
		return
	}
	if !isAuthenticated(r, m.cfg.Password, m.cookieToken) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="tailmon"`)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if m.cfg.TerminalPin != "" && !isTerminalPinUnlocked(r, m.cfg.TerminalPin, m.pinCookieToken) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "pin_required"})
		return
	}

	device := strings.TrimSpace(r.URL.Query().Get("device"))
	target, err := m.collector.ResolveTerminalTarget(device)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}

	cols := clampTermDim(r.URL.Query().Get("cols"), 20, 400, 100)
	rows := clampTermDim(r.URL.Query().Get("rows"), 5, 200, 30)

	sessCtx, sessCancel := context.WithCancel(r.Context())
	defer sessCancel()

	sessID, ok := m.acquireSlot(target.DisplayName, sessCancel)
	if !ok {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "max concurrent terminal sessions reached"})
		return
	}
	defer m.releaseSlot(sessID)

	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		log.Printf("terminal ws accept failed (%s): %v", target.DisplayName, err)
		return
	}
	defer ws.CloseNow()

	var cmd *exec.Cmd
	if target.IsLocal {
		sh := resolveLocalShell()
		cmd = exec.Command(sh, "-l")
	} else {
		args := []string{
			"-tt",
			"-o", "StrictHostKeyChecking=accept-new",
			"-o", "ServerAliveInterval=15",
			"-o", "ServerAliveCountMax=3",
		}
		args = append(args, target.SSHFields...)
		cmd = exec.Command("ssh", args...)
	}
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{
		Rows: rows,
		Cols: cols,
	})
	if err != nil {
		log.Printf("terminal pty start failed (%s): %v", target.DisplayName, err)
		_ = ws.Close(websocket.StatusInternalError, "failed to start terminal")
		return
	}
	m.attachPTY(sessID, ptmx)

	startedAt := time.Now()
	log.Printf("terminal session %s started: %s (local=%v)", sessID, target.DisplayName, target.IsLocal)

	defer func() {
		_ = ptmx.Close()
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGHUP)
			doneCh := make(chan struct{})
			go func() {
				_ = cmd.Wait()
				close(doneCh)
			}()
			select {
			case <-doneCh:
			case <-time.After(2 * time.Second):
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				<-doneCh
			}
		}
		log.Printf("terminal session %s ended: %s (duration=%s)", sessID, target.DisplayName, time.Since(startedAt).Round(time.Second))
	}()

	idleTimeout := m.cfg.TermIdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = defaultTermIdleTimeout
	}
	idleCh := make(chan struct{}, 1)
	touchIdle := func() {
		select {
		case idleCh <- struct{}{}:
		default:
		}
	}

	go func() {
		timer := time.NewTimer(idleTimeout)
		defer timer.Stop()
		for {
			select {
			case <-sessCtx.Done():
				return
			case <-idleCh:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(idleTimeout)
			case <-timer.C:
				log.Printf("terminal session %s idle timeout (%s)", sessID, target.DisplayName)
				_ = ws.Close(websocket.StatusNormalClosure, "idle timeout")
				sessCancel()
				return
			}
		}
	}()

	errCh := make(chan error, 2)

	// PTY -> WebSocket
	go func() {
		buf := make([]byte, 4096)
		for {
			n, readErr := ptmx.Read(buf)
			if n > 0 {
				touchIdle()
				if writeErr := ws.Write(sessCtx, websocket.MessageBinary, buf[:n]); writeErr != nil {
					errCh <- writeErr
					return
				}
			}
			if readErr != nil {
				errCh <- readErr
				return
			}
		}
	}()

	// WebSocket -> PTY
	go func() {
		for {
			msgType, data, readErr := ws.Read(sessCtx)
			if readErr != nil {
				errCh <- readErr
				return
			}
			touchIdle()
			switch msgType {
			case websocket.MessageBinary:
				if len(data) > 0 {
					if _, writeErr := ptmx.Write(data); writeErr != nil {
						errCh <- writeErr
						return
					}
				}
			case websocket.MessageText:
				var ctrl termControlMsg
				if err := json.Unmarshal(data, &ctrl); err == nil {
					switch ctrl.Type {
					case "resize":
						c := clampTermDim(strconv.Itoa(int(ctrl.Cols)), 20, 400, cols)
						r := clampTermDim(strconv.Itoa(int(ctrl.Rows)), 5, 200, rows)
						_ = pty.Setsize(ptmx, &pty.Winsize{Rows: r, Cols: c})
					case "ping":
						// Keepalive/idle-reset handled by touchIdle().
					}
				}
			}
		}
	}()

	<-errCh
	sessCancel()
	_ = ws.Close(websocket.StatusNormalClosure, "session closed")
}
