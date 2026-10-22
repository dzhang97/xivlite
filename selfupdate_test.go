package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestNewerVersion(t *testing.T) {
	for _, c := range []struct {
		tag, cur string
		want     bool
	}{{"v1.1.0", "1.0.0", true}, {"v1.0.0", "1.0.0", false}, {"v1.0.9", "1.1.0", false}, {"v2.0.0-rc1", "1.9.9", true}, {"v1.10.0", "1.9.0", true}} {
		if newerVersion(c.tag, c.cur) != c.want {
			t.Errorf("%s vs %s", c.tag, c.cur)
		}
	}
}

func releaseArchive(t *testing.T, files map[string]string) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, body := range files {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

func TestSelfUpdate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".local", "share", "xivlite")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "xivlite"), []byte("old binary"), 0o755)
	os.WriteFile(filepath.Join(dir, "xivlite-wrap"), []byte("old wrapper"), 0o755)

	archive := releaseArchive(t, map[string]string{"xivlite/xivlite": "new binary", "xivlite/xivlite-wrap": "new wrapper", "xivlite/README.md": "x"})
	sum := sha256.Sum256(archive)
	sums := hex.EncodeToString(sum[:]) + "  " + releaseAsset + "\n"
	tag := "v9.0.0"
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":%q,"assets":[{"name":%q,"browser_download_url":"%s/a"},{"name":"SHA256SUMS","browser_download_url":"%s/s"}]}`,
			tag, releaseAsset, srv.URL, srv.URL)
	})
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) { w.Write(archive) })
	mux.HandleFunc("/s", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(sums)) })
	srv = httptest.NewServer(mux)
	defer srv.Close()
	saved := releasesAPI
	releasesAPI = srv.URL + "/latest"
	defer func() { releasesAPI = saved }()

	sums = "0000  " + releaseAsset + "\n"
	if err := selfUpdate(false); err == nil {
		t.Fatal("a checksum mismatch was accepted")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "xivlite")); string(b) != "old binary" {
		t.Fatal("files changed despite the checksum mismatch")
	}

	sums = hex.EncodeToString(sum[:]) + "  " + releaseAsset + "\n"
	if err := selfUpdate(false); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"xivlite": "new binary", "xivlite-wrap": "new wrapper"} {
		if b, _ := os.ReadFile(filepath.Join(dir, name)); string(b) != want {
			t.Errorf("%s = %q", name, b)
		}
	}

	tag = "v" + version
	os.WriteFile(filepath.Join(dir, "xivlite"), []byte("current"), 0o755)
	if err := selfUpdate(false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "xivlite")); string(b) != "current" {
		t.Error("updated although already current")
	}
}
