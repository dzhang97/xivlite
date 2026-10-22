//go:build xivlite_mock

// A stand-in for Square Enix's login and patch servers, built only with -tags xivlite_mock.
// Tests use it directly; a mock binary starts it when XIVLITE_MOCK is set (ok, patch, bootpatch, closed).

package main

import (
	"bytes"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
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

type mockPatch struct {
	repo, version string
	data          []byte
}

type mockSE struct {
	srv        *httptest.Server
	password   string
	otpKey     []byte
	gateClosed bool
	patches    []mockPatch

	mu         sync.Mutex
	lastReport string
	lastForm   map[string]string
	downloads  int
	failures   []string
}

const (
	mockUID       = "c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00"
	mockHashBlock = 4096
)

var reportLine = regexp.MustCompile(`^[0-9.]+=ffxivboot\.exe/\d+/[0-9a-f]{40},ffxivboot64\.exe/\d+/[0-9a-f]{40},ffxivlauncher64\.exe/\d+/[0-9a-f]{40},ffxivupdater64\.exe/\d+/[0-9a-f]{40}$`)

func (m *mockSE) fail(format string, a ...any) {
	m.mu.Lock()
	m.failures = append(m.failures, fmt.Sprintf(format, a...))
	m.mu.Unlock()
}

// The current release: what fakeInstall lays out, and what a fresh install must end up with.
func mockRelease() []mockPatch {
	boot := newZiPatch()
	boot.targetInfo(0)
	for _, exe := range []string{"ffxivboot.exe", "ffxivboot64.exe", "ffxivlauncher64.exe", "ffxivupdater64.exe", "ffxivconfig64.exe"} {
		boot.addFile(exe, 0, []byte("MZ boot "+exe))
	}
	game := newZiPatch()
	game.targetInfo(0)
	game.addFile("ffxiv_dx11.exe", 0, bytes.Repeat([]byte("MZ game "), 3000))
	game.dir("ADIR", "/sqpack/ffxiv")
	game.header('D', 'V', 0x0a, 0x0000, 0, []byte("SqPack dat"))
	game.addData(0x0a, 0x0000, 0, 8, bytes.Repeat([]byte{7}, 1024), 0)
	patches := []mockPatch{{"boot", "2026.08.20.0000.0001", boot.bytes()}, {"game", "2026.10.01.0000.0000", game.bytes()}}
	for i := 1; i <= 5; i++ {
		ex := newZiPatch()
		ex.targetInfo(0)
		ex.addFile(fmt.Sprintf("sqpack/ex%d/0c0%d00.win32.dat0", i, i), 0, bytes.Repeat([]byte{byte(i)}, 5000))
		patches = append(patches, mockPatch{fmt.Sprintf("ex%d", i), fmt.Sprintf("2026.09.01.0000.000%d", i), ex.bytes()})
	}
	return patches
}

// addHotfix publishes a newer patch for repo, which installs at the current version then need.
func (m *mockSE) addHotfix(repo string) {
	p := newZiPatch()
	p.targetInfo(0)
	if repo == "boot" {
		p.addFile("ffxivboot64.exe", 0, []byte("MZ boot ffxivboot64.exe, hotfixed"))
		m.patches = append(m.patches, mockPatch{"boot", "2026.10.20.0000.0001", p.bytes()})
		return
	}
	p.addFile("ffxiv_dx11.exe", 0, bytes.Repeat([]byte("MZ game, hotfixed "), 2000))
	m.patches = append(m.patches, mockPatch{"game", "2026.10.15.0000.0000", p.bytes()})
}

func (m *mockSE) patchURL(p mockPatch) string {
	dir := map[string]string{"boot": "boot/2b5cbc63", "game": "game/4e9a232b"}[p.repo]
	if dir == "" {
		dir = "game/" + p.repo + "/6b936f08"
	}
	return fmt.Sprintf("%s/patch/%s/D%s.patch", m.srv.URL, dir, p.version)
}

// patchList answers like SE's servers: a multipart body with one tab-separated row per patch.
func (m *mockSE) patchList(have map[string]string) string {
	var rows []string
	for _, p := range m.patches {
		// The boot server lists boot patches only, the game server everything else.
		if _, asked := have[p.repo]; !asked || p.version <= have[p.repo] {
			continue
		}
		if p.repo == "boot" {
			rows = append(rows, fmt.Sprintf("%d\t%d\t1\t1\t%s\t%s", len(p.data), len(p.data)*2, p.version, m.patchURL(p)))
			continue
		}
		var hashes []string
		for off := 0; off < len(p.data); off += mockHashBlock {
			sum := sha1.Sum(p.data[off:min(off+mockHashBlock, len(p.data))])
			hashes = append(hashes, hex.EncodeToString(sum[:]))
		}
		rows = append(rows, fmt.Sprintf("%d\t%d\t1\t1\t%s\tsha1\t%d\t%s\t%s",
			len(p.data), len(p.data)*2, p.version, mockHashBlock, strings.Join(hashes, ","), m.patchURL(p)))
	}
	if len(rows) == 0 {
		return ""
	}
	return "--477D80B1_38BC_41d4_8B48_5273ADB89CAC\r\nContent-Type: application/octet-stream\r\n" +
		"Content-Location: ffxivpatch/4e9a232b/metainventory/D.http\r\nX-Patch-Length: 1\r\n\r\n" +
		strings.Join(rows, "\r\n") + "\r\n--477D80B1_38BC_41d4_8B48_5273ADB89CAC--\r\n"
}

func startMockSE() *mockSE {
	m := &mockSE{password: "hunter2", patches: mockRelease()}
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
		ver := strings.Trim(strings.TrimPrefix(r.URL.Path, "/boot/"), "/")
		io.WriteString(w, m.patchList(map[string]string{"boot": ver}))
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
		parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/game/"), "/"), "/")
		lines := strings.Split(string(body), "\n")
		if r.Header.Get("X-Hash-Check") != "enabled" || r.UserAgent() != patcherUserAgent || len(parts) != 2 || parts[1] != "MOCKSESSION" ||
			len(lines) != 7 || !reportLine.MatchString(lines[0]) {
			m.fail("gamever: path %s report %q", r.URL.Path, body)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		have := map[string]string{"game": parts[0]}
		for i, line := range lines[1:6] {
			repo, ver, _ := strings.Cut(line, "\t")
			if repo != fmt.Sprintf("ex%d", i+1) {
				m.fail("gamever: report line %q", line)
			}
			have[repo] = ver
		}
		w.Header().Set("X-Patch-Unique-Id", mockUID)
		io.WriteString(w, m.patchList(have))
	})
	mux.HandleFunc("/patch/", func(w http.ResponseWriter, r *http.Request) {
		for _, p := range m.patches {
			if strings.HasSuffix(r.URL.Path, "/D"+p.version+".patch") && r.UserAgent() == patcherUserAgent {
				m.mu.Lock()
				m.downloads++
				m.mu.Unlock()
				http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(p.data))
				return
			}
		}
		http.NotFound(w, r)
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
	switch mode {
	case "patch":
		m.addHotfix("game")
	case "bootpatch":
		m.addHotfix("boot")
	case "closed":
		m.gateClosed = true
	}
	fmt.Fprintln(os.Stderr, "xivlite: MOCK BUILD - talking to a fake Square Enix at", m.srv.URL)
}
