package main

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"crypto/sha1"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// endpoints are variables only so tests can point them at a local server.
var endpoints = struct {
	gateStatus, oauthTop, oauthSend, bootVer, gameVer, frontierReferer string
}{
	gateStatus:      "https://frontier.ffxiv.com/worldStatus/gate_status.json",
	oauthTop:        "https://ffxiv-login.square-enix.com/oauth/ffxivarr/login/top",
	oauthSend:       "https://ffxiv-login.square-enix.com/oauth/ffxivarr/login/login.send",
	bootVer:         "http://patch-bootver.ffxiv.com/http/win32/ffxivneo_release_boot/",
	gameVer:         "https://patch-gamever.ffxiv.com/http/win32/ffxivneo_release_game/",
	frontierReferer: "https://launcher.finalfantasyxiv.com/v740/index.html?rc_lang={0}&time={1}",
}

// tlsConfig is nil (system roots) except in tests and mock builds.
var tlsConfig *tls.Config

const (
	patcherUserAgent = "FFXIV PATCH CLIENT"
	ieAccept         = "image/gif, image/jpeg, image/pjpeg, application/x-ms-application, application/xaml+xml, application/x-ms-xbap, */*"
)

var (
	errBootPatch = errors.New("the game's boot files need an update")
	errGamePatch = errors.New("the game needs an update")
	storedRe     = regexp.MustCompile(`\t<\s*input .* name="_STORED_" value="(.*)">`)
	loginOkRe    = regexp.MustCompile(`window.external.user\("login=auth,ok,(.*)\);`)
	loginErrRe   = regexp.MustCompile(`window.external.user\("login=auth,ng,err,(.*)"\);`)
)

type language int

const (
	langJapanese language = iota
	langEnglish
	langGerman
	langFrench
)

func (l language) code() string {
	return [...]string{"ja", "en-us", "de", "fr"}[l]
}

type session struct {
	http           *http.Client
	userAgent      string
	acceptLanguage string
	lang           language
}

func newSession(acceptLanguage string, lang language) *session {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ForceAttemptHTTP2 = false
	transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	transport.DisableCompression = true
	if tlsConfig != nil {
		transport.TLSClientConfig = tlsConfig
	}
	return &session{
		http: &http.Client{
			Transport: transport,
			Timeout:   45 * time.Second,
		},
		userAgent:      fmt.Sprintf("SQEXAuthor/2.0.0(Windows 6.2; ja-jp; %s)", computerID()),
		acceptLanguage: acceptLanguage,
		lang:           lang,
	}
}

// computerID is MakeComputerId: a checksum byte then four bytes of a SHA-1 of machine facts.
func computerID() string {
	host, _ := os.Hostname()
	name := ""
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	facts := host + name + "Unix " + runtime.GOOS + strconv.Itoa(runtime.NumCPU())
	units := utf16.Encode([]rune(facts))
	raw := make([]byte, 2*len(units))
	for i, u := range units {
		binary.LittleEndian.PutUint16(raw[2*i:], u)
	}
	sum := sha1.Sum(raw)
	id := []byte{0, sum[0], sum[1], sum[2], sum[3]}
	id[0] = -(id[1] + id[2] + id[3] + id[4])
	return hex.EncodeToString(id)
}

func utcStamp() string { return time.Now().UTC().Format("2006-01-02-15-04") }

func (s *session) frontierReferer() string {
	return strings.NewReplacer("{0}", strings.ReplaceAll(s.lang.code(), "-", "_"), "{1}", utcStamp()).
		Replace(endpoints.frontierReferer)
}

func (s *session) do(req *http.Request, headers [][2]string) (*http.Response, string, error) {
	for _, h := range headers {
		if h[0] == "Host" {
			req.Host = h[1]
			continue
		}
		req.Header.Set(h[0], h[1])
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	var body io.Reader = resp.Body
	switch strings.ToLower(resp.Header.Get("Content-Encoding")) {
	case "gzip":
		zr, err := gzip.NewReader(resp.Body)
		if err != nil {
			return resp, "", err
		}
		body = zr
	case "deflate":
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			return resp, "", err
		}
		if zr, zerr := zlib.NewReader(bytes.NewReader(raw)); zerr == nil {
			body = zr
		} else {
			body = flate.NewReader(bytes.NewReader(raw))
		}
	}
	b, err := io.ReadAll(io.LimitReader(body, 8<<20))
	return resp, string(b), err
}

// gateOpen reports whether the login gate is open (false during maintenance).
func (s *session) gateOpen() (bool, error) {
	u := fmt.Sprintf("%s?lang=%s&_=%d", endpoints.gateStatus, s.lang.code(), time.Now().UnixMilli())
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	resp, body, err := s.do(req, [][2]string{
		{"User-Agent", s.userAgent},
		{"Accept-Encoding", "gzip, deflate"},
		{"Accept-Language", s.acceptLanguage},
		{"Origin", "https://launcher.finalfantasyxiv.com"},
		{"Referer", s.frontierReferer()},
		{"Connection", "Keep-Alive"},
	})
	if err != nil {
		return false, err
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("gate status: HTTP %d", resp.StatusCode)
	}
	var gate struct {
		Status json.RawMessage `json:"status"`
	}
	if err := json.Unmarshal([]byte(body), &gate); err != nil {
		return false, fmt.Errorf("gate status: %v", err)
	}
	v := strings.TrimSpace(string(gate.Status))
	return v == "1" || v == "true", nil
}

// bootNeedsPatch asks the boot patch server whether boot/ is current; any patch list means it is not.
func (s *session) bootNeedsPatch(g install) (bool, error) {
	stamp := []byte(utcStamp())
	stamp[15] = '0'
	u := endpoints.bootVer + g.ver("boot") + "/?time=" + string(stamp)
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	host := ""
	if parsed, err := url.Parse(u); err == nil {
		host = parsed.Host
	}
	resp, body, err := s.do(req, [][2]string{{"User-Agent", patcherUserAgent}, {"Host", host}})
	if err != nil {
		return false, err
	}
	if resp.StatusCode >= 400 {
		return false, fmt.Errorf("boot version check: HTTP %d", resp.StatusCode)
	}
	return strings.TrimSpace(body) != "", nil
}

type oauthResult struct {
	sessionID     string
	region        int
	termsAccepted bool
	playable      bool
	maxExpansion  int
}

func (s *session) oauthLogin(username, password, otp string, freeTrial bool) (*oauthResult, error) {
	isft := "0"
	if freeTrial {
		isft = "1"
	}
	topURL := fmt.Sprintf("%s?lng=en&rgn=3&isft=%s&cssmode=1&isnew=1&launchver=3", endpoints.oauthTop, isft)
	req, _ := http.NewRequest(http.MethodGet, topURL, nil)
	_, top, err := s.do(req, [][2]string{
		{"Accept", ieAccept},
		{"Referer", s.frontierReferer()},
		{"Accept-Encoding", "gzip, deflate"},
		{"Accept-Language", s.acceptLanguage},
		{"User-Agent", s.userAgent},
		{"Connection", "Keep-Alive"},
		{"Cookie", `_rsid=""`},
	})
	if err != nil {
		return nil, fmt.Errorf("login page: %v", err)
	}
	if strings.Contains(top, `window.external.user("restartup");`) {
		return nil, errors.New("this account is a Steam service account; it cannot log in without Steam")
	}
	m := storedRe.FindStringSubmatch(top)
	if m == nil {
		return nil, errors.New("login page did not contain the expected form (_STORED_); Square Enix may have changed it")
	}

	form := "_STORED_=" + url.QueryEscape(m[1]) +
		"&sqexid=" + url.QueryEscape(username) +
		"&password=" + url.QueryEscape(password) +
		"&otppw=" + url.QueryEscape(otp)
	req, _ = http.NewRequest(http.MethodPost, endpoints.oauthSend, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	sendHost := ""
	if parsed, err := url.Parse(endpoints.oauthSend); err == nil {
		sendHost = parsed.Host
	}
	_, reply, err := s.do(req, [][2]string{
		{"Accept", ieAccept},
		{"Referer", topURL},
		{"Accept-Language", s.acceptLanguage},
		{"User-Agent", s.userAgent},
		{"Accept-Encoding", "gzip, deflate"},
		{"Host", sendHost},
		{"Connection", "Keep-Alive"},
		{"Cache-Control", "no-cache"},
		{"Cookie", `_rsid=""`},
	})
	if err != nil {
		return nil, fmt.Errorf("login: %v", err)
	}
	ok := loginOkRe.FindStringSubmatch(reply)
	if ok == nil {
		if e := loginErrRe.FindStringSubmatch(reply); e != nil {
			return nil, fmt.Errorf("Square Enix refused the login: %s", e[1])
		}
		return nil, errors.New("Square Enix returned an unrecognised login reply")
	}
	p := strings.Split(ok[1], ",")
	if len(p) < 14 {
		return nil, fmt.Errorf("login reply has %d fields, expected at least 14", len(p))
	}
	region, err1 := strconv.Atoi(p[5])
	maxEx, err2 := strconv.Atoi(strings.Trim(p[13], `"`))
	if err1 != nil || err2 != nil {
		return nil, errors.New("login reply has a malformed region or expansion")
	}
	return &oauthResult{
		sessionID:     p[1],
		region:        region,
		termsAccepted: p[3] != "0",
		playable:      p[9] != "0",
		maxExpansion:  maxEx,
	}, nil
}

// registerSession trades the OAuth session for the game session id (X-Patch-Unique-Id).
func (s *session) registerSession(g install, o *oauthResult) (string, error) {
	if err := g.checkVersionSanity(o.maxExpansion); err != nil {
		return "", err
	}
	report, err := g.versionReport(o.maxExpansion)
	if err != nil {
		return "", err
	}
	req, _ := http.NewRequest(http.MethodPost, endpoints.gameVer+g.ver("game")+"/"+o.sessionID, strings.NewReader(report))
	resp, body, err := s.do(req, [][2]string{
		{"Connection", "Keep-Alive"},
		{"User-Agent", patcherUserAgent},
		{"X-Hash-Check", "enabled"},
	})
	if err != nil {
		return "", fmt.Errorf("session registration: %v", err)
	}
	switch resp.StatusCode {
	case http.StatusConflict:
		return "", fmt.Errorf("%w (the server rejected the boot file hashes; reinstall or repair the game)", errBootPatch)
	case http.StatusGone:
		return "", errors.New("the server no longer serves this game version; update the game")
	}
	uid := resp.Header.Get("X-Patch-Unique-Id")
	if uid == "" {
		return "", fmt.Errorf("session registration: no X-Patch-Unique-Id (HTTP %d)", resp.StatusCode)
	}
	if strings.TrimSpace(body) != "" {
		return "", errGamePatch
	}
	return uid, nil
}
