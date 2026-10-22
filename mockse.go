//go:build xivlite_mock

// A stand-in for Square Enix's login and patch servers, built only with -tags xivlite_mock.
// Tests use it directly; a mock binary starts it when XIVLITE_MOCK is set (ok, patch, bootpatch, closed).

package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

type mockSE struct {
	srv        *httptest.Server
	password   string
	otpKey     []byte
	gateClosed bool
	bootPatch  bool
	gamePatch  bool

	mu         sync.Mutex
	lastReport string
	lastForm   map[string]string
	failures   []string
}

const mockUID = "c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00"

var reportLine = regexp.MustCompile(`^[0-9.]+=ffxivboot\.exe/\d+/[0-9a-f]{40},ffxivboot64\.exe/\d+/[0-9a-f]{40},ffxivlauncher64\.exe/\d+/[0-9a-f]{40},ffxivupdater64\.exe/\d+/[0-9a-f]{40}$`)

func (m *mockSE) fail(format string, a ...any) {
	m.mu.Lock()
	m.failures = append(m.failures, fmt.Sprintf(format, a...))
	m.mu.Unlock()
}

func startMockSE() *mockSE {
	m := &mockSE{password: "hunter2"}
	mux := http.NewServeMux()
	mux.HandleFunc("/worldStatus/gate_status.json", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.UserAgent(), "SQEXAuthor/2.0.0(Windows 6.2; ja-jp; ") {
			m.fail("gate: user agent %q", r.UserAgent())
		}
		status := 1
		if m.gateClosed {
			status = 0
		}
		fmt.Fprintf(w, `{"status":%d,"message":[],"news":[]}`, status)
	})
	mux.HandleFunc("/boot/", func(w http.ResponseWriter, r *http.Request) {
		if r.UserAgent() != patcherUserAgent || !regexp.MustCompile(`^\d{4}-\d\d-\d\d-\d\d-\d0$`).MatchString(r.URL.Query().Get("time")) {
			m.fail("bootver: ua %q time %q", r.UserAgent(), r.URL.Query().Get("time"))
		}
		if m.bootPatch {
			io.WriteString(w, "--mock patch list--")
		}
	})
	mux.HandleFunc("/oauth/top", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != `_rsid=""` || r.URL.Query().Get("rgn") != "3" {
			m.fail("top: cookie %q query %q", r.Header.Get("Cookie"), r.URL.RawQuery)
		}
		io.WriteString(w, "<html><body><form>\n\t<input type=\"hidden\" name=\"_STORED_\" value=\"st0red+value/=\">\n</form></body></html>")
	})
	mux.HandleFunc("/oauth/send", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			m.fail("send: %v", err)
		}
		form := map[string]string{}
		for k := range r.PostForm {
			form[k] = r.PostForm.Get(k)
		}
		m.mu.Lock()
		m.lastForm = form
		m.mu.Unlock()
		otpOK := m.otpKey == nil || form["otppw"] == totpAt(m.otpKey, time.Now()) || form["otppw"] == totpAt(m.otpKey, time.Now().Add(-30*time.Second))
		switch {
		case form["_STORED_"] != "st0red+value/=":
			io.WriteString(w, `window.external.user("login=auth,ng,err,Bad form.");`)
		case form["password"] != m.password || !otpOK:
			io.WriteString(w, `<script>window.external.user("login=auth,ng,err,The Square Enix ID or password is incorrect.");</script>`)
		default:
			io.WriteString(w, `<script>window.external.user("login=auth,ok,sid,MOCKSESSION,terms,1,region,3,etmadd,0,playable,1,ps3pkg,0,maxex,5,product,1");</script>`)
		}
	})
	mux.HandleFunc("/game/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		m.lastReport = string(body)
		m.mu.Unlock()
		lines := strings.Split(string(body), "\n")
		if r.Header.Get("X-Hash-Check") != "enabled" || r.UserAgent() != patcherUserAgent || !strings.HasSuffix(r.URL.Path, "/MOCKSESSION") ||
			len(lines) != 7 || !reportLine.MatchString(lines[0]) || lines[5] != "ex5\t2026.09.01.0000.0005" {
			m.fail("gamever: path %s report %q", r.URL.Path, body)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("X-Patch-Unique-Id", mockUID)
		if m.gamePatch {
			io.WriteString(w, "--mock patch list--")
		}
	})
	m.srv = httptest.NewTLSServer(mux)
	pool := x509.NewCertPool()
	pool.AddCert(m.srv.Certificate())
	tlsConfig = &tls.Config{RootCAs: pool}
	base := m.srv.URL
	endpoints.gateStatus = base + "/worldStatus/gate_status.json"
	endpoints.oauthTop = base + "/oauth/top"
	endpoints.oauthSend = base + "/oauth/send"
	endpoints.bootVer = base + "/boot/"
	endpoints.gameVer = base + "/game/"
	return m
}

func init() {
	mode := os.Getenv("XIVLITE_MOCK")
	if mode == "" {
		return
	}
	m := startMockSE()
	m.gamePatch = mode == "patch"
	m.bootPatch = mode == "bootpatch"
	m.gateClosed = mode == "closed"
	fmt.Fprintln(os.Stderr, "xivlite: MOCK BUILD - talking to a fake Square Enix at", m.srv.URL)
}
