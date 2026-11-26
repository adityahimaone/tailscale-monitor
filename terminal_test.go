package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseSSHTargets(t *testing.T) {
	got := parseSSHTargets(" db-vps = root@100.64.0.5 , worker=ssh -p 2222 ubuntu@worker-ts , , badentry, empty= ")
	if len(got) != 2 {
		t.Fatalf("expected 2 parsed targets, got %d (%v)", len(got), got)
	}
	if got["db-vps"] != "root@100.64.0.5" {
		t.Fatalf("unexpected db-vps target: %q", got["db-vps"])
	}
	if got["worker"] != "ssh -p 2222 ubuntu@worker-ts" {
		t.Fatalf("unexpected worker target: %q", got["worker"])
	}
}

func TestResolveTerminalTarget(t *testing.T) {
	cfg := Config{
		SSHTargets: map[string]string{
			"db-vps": "ssh -p 2222 root@100.64.0.5",
		},
	}
	c := NewCollector(cfg)
	c.mu.Lock()
	c.snapshot.Devices = []Device{
		{
			Hostname:    "hub-vps",
			DNSName:     "hub-vps.tail.ts.net",
			TailscaleIP: "100.64.0.1",
			OS:          "linux",
			Online:      true,
			IsSelf:      true,
		},
		{
			Hostname:    "db-vps",
			DNSName:     "db-vps.tail.ts.net",
			TailscaleIP: "100.64.0.5",
			OS:          "linux",
			Online:      true,
			IsSelf:      false,
		},
		{
			Hostname:    "offline-node",
			DNSName:     "offline-node.tail.ts.net",
			TailscaleIP: "100.64.0.9",
			OS:          "linux",
			Online:      false,
			IsSelf:      false,
		},
		{
			Hostname:    "iphone",
			DNSName:     "iphone.tail.ts.net",
			TailscaleIP: "100.64.0.10",
			OS:          "iOS",
			Online:      true,
			IsSelf:      false,
		},
	}
	c.mu.Unlock()

	localTarget, err := c.ResolveTerminalTarget("100.64.0.1")
	if err != nil {
		t.Fatalf("expected self target to resolve, got err: %v", err)
	}
	if !localTarget.IsLocal || localTarget.DisplayName != "hub-vps" {
		t.Fatalf("unexpected self target: %+v", localTarget)
	}

	remoteTarget, err := c.ResolveTerminalTarget("db-vps")
	if err != nil {
		t.Fatalf("expected db-vps to resolve, got err: %v", err)
	}
	if remoteTarget.IsLocal {
		t.Fatalf("expected remote target, got local")
	}
	wantFields := []string{"-p", "2222", "root@100.64.0.5"}
	if len(remoteTarget.SSHFields) != len(wantFields) {
		t.Fatalf("unexpected SSHFields: %v", remoteTarget.SSHFields)
	}
	for i := range wantFields {
		if remoteTarget.SSHFields[i] != wantFields[i] {
			t.Fatalf("SSHFields[%d] = %q, want %q", i, remoteTarget.SSHFields[i], wantFields[i])
		}
	}

	if _, err := c.ResolveTerminalTarget("offline-node"); err == nil {
		t.Fatalf("expected error for offline node")
	}
	if _, err := c.ResolveTerminalTarget("iphone"); err == nil {
		t.Fatalf("expected error for unconfigured node")
	}
	if _, err := c.ResolveTerminalTarget("100.99.99.99;id"); err == nil {
		t.Fatalf("expected error for arbitrary unallowlisted target")
	}
}

func TestTerminalSecurityGates(t *testing.T) {
	// 1. Disabled when TAILMON_PASSWORD is empty
	cNoAuth := NewCollector(Config{})
	tmNoAuth := NewTerminalManager(Config{}, cNoAuth, "", "")
	req := httptest.NewRequest(http.MethodGet, "/api/terminal/ws?device=hub-vps", nil)
	rec := httptest.NewRecorder()
	tmNoAuth.HandleWS(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when password is empty, got %d", rec.Code)
	}

	// 2. Auth + PIN gate
	cfg := Config{
		Password:        "secret-pass",
		TerminalPin:     "9944",
		TermMaxSessions: 2,
	}
	cookieTok := computeAuthToken(cfg.Password)
	pinTok := computeTerminalPinToken(cfg.TerminalPin)
	c := NewCollector(cfg)
	tm := NewTerminalManager(cfg, c, cookieTok, pinTok)

	// Unauthenticated -> 401
	reqUnauth := httptest.NewRequest(http.MethodGet, "/api/terminal/ws?device=hub-vps", nil)
	recUnauth := httptest.NewRecorder()
	tm.HandleWS(recUnauth, reqUnauth)
	if recUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when unauthenticated, got %d", recUnauth.Code)
	}

	// Authenticated without PIN cookie -> 403 pin_required
	reqNoPin := httptest.NewRequest(http.MethodGet, "/api/terminal/ws?device=hub-vps", nil)
	reqNoPin.Header.Set("Authorization", "Bearer "+cfg.Password)
	recNoPin := httptest.NewRecorder()
	tm.HandleWS(recNoPin, reqNoPin)
	if recNoPin.Code != http.StatusForbidden || !strings.Contains(recNoPin.Body.String(), "pin_required") {
		t.Fatalf("expected 403 pin_required, got %d %s", recNoPin.Code, recNoPin.Body.String())
	}

	// Unlock with wrong PIN -> 401
	reqBadPin := httptest.NewRequest(http.MethodPost, "/api/terminal/unlock", bytes.NewBufferString(`{"pin":"0000"}`))
	reqBadPin.Header.Set("Authorization", "Bearer "+cfg.Password)
	recBadPin := httptest.NewRecorder()
	tm.HandleUnlock(recBadPin, reqBadPin)
	if recBadPin.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong pin, got %d", recBadPin.Code)
	}

	// Unlock with right PIN -> 200 + cookie
	reqGoodPin := httptest.NewRequest(http.MethodPost, "/api/terminal/unlock", bytes.NewBufferString(`{"pin":"9944"}`))
	reqGoodPin.Header.Set("Authorization", "Bearer "+cfg.Password)
	recGoodPin := httptest.NewRecorder()
	tm.HandleUnlock(recGoodPin, reqGoodPin)
	if recGoodPin.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid pin, got %d", recGoodPin.Code)
	}
	cookies := recGoodPin.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Name != termPinCookieName {
		t.Fatalf("expected %s cookie to be set, got %+v", termPinCookieName, cookies)
	}

	// Status endpoint reflects unlocked state
	reqStatus := httptest.NewRequest(http.MethodGet, "/api/terminal/status", nil)
	reqStatus.Header.Set("Authorization", "Bearer "+cfg.Password)
	reqStatus.AddCookie(cookies[0])
	recStatus := httptest.NewRecorder()
	tm.HandleStatus(recStatus, reqStatus)
	if recStatus.Code != http.StatusOK {
		t.Fatalf("expected 200 from status, got %d", recStatus.Code)
	}
	var st map[string]any
	if err := json.Unmarshal(recStatus.Body.Bytes(), &st); err != nil {
		t.Fatalf("invalid status json: %v", err)
	}
	if st["enabled"] != true || st["pin_required"] != true || st["pin_unlocked"] != true {
		t.Fatalf("unexpected status payload: %+v", st)
	}
}

func TestTerminalMaxSessions(t *testing.T) {
	cfg := Config{TermMaxSessions: 2}
	tm := NewTerminalManager(cfg, NewCollector(cfg), "", "")

	id1, ok1 := tm.acquireSlot("n1", func() {})
	id2, ok2 := tm.acquireSlot("n2", func() {})
	_, ok3 := tm.acquireSlot("n3", func() {})

	if !ok1 || !ok2 {
		t.Fatalf("expected first 2 slots to succeed")
	}
	if ok3 {
		t.Fatalf("expected 3rd slot to be rejected at max_sessions=2")
	}

	tm.releaseSlot(id1)
	id4, ok4 := tm.acquireSlot("n4", func() {})
	if !ok4 || id4 == "" {
		t.Fatalf("expected slot after release to succeed")
	}
	tm.releaseSlot(id2)
	tm.releaseSlot(id4)
}
