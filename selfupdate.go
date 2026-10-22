package main

// `xivlite self-update`: replaces xivlite and xivlite-wrap with the newest GitHub release.

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const releaseAsset = "xivlite-linux-x86_64.tar.gz"

// releasesAPI is a variable only so tests can point it at a local server.
var releasesAPI = "https://api.github.com/repos/dzhang97/xivlite/releases/latest"

type release struct {
	Tag    string `json:"tag_name"`
	Assets []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// newerVersion reports whether tag (v1.2.3) is newer than the running version.
func newerVersion(tag, current string) bool {
	parse := func(v string) [3]int {
		var out [3]int
		for i, part := range strings.SplitN(strings.TrimPrefix(v, "v"), ".", 3) {
			n, _ := strconv.Atoi(strings.TrimFunc(part, func(r rune) bool { return r < '0' || r > '9' }))
			out[i] = n
		}
		return out
	}
	a, b := parse(tag), parse(current)
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

func updateClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if tlsConfig != nil {
		transport.TLSClientConfig = tlsConfig
	}
	return &http.Client{Transport: transport, Timeout: 5 * time.Minute}
}

func getBytes(c *http.Client, url string) ([]byte, error) {
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("User-Agent", "xivlite/"+version)
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

// installDir is where the running xivlite lives; FEX may report its own binary, so the folder
// must hold xivlite-wrap too, else the default install folder is used.
func installDir() (string, error) {
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			dir := filepath.Dir(real)
			if _, err := os.Stat(filepath.Join(dir, "xivlite-wrap")); err == nil && filepath.Base(real) == "xivlite" {
				return dir, nil
			}
		}
	}
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".local", "share", "xivlite")
	if _, err := os.Stat(filepath.Join(dir, "xivlite")); err != nil {
		return "", errors.New("cannot find the installed xivlite; run install.sh from a release instead")
	}
	return dir, nil
}

func selfUpdate(force bool) error {
	c := updateClient()
	body, err := getBytes(c, releasesAPI)
	if err != nil {
		return fmt.Errorf("checking for a release: %v", err)
	}
	var rel release
	if err := json.Unmarshal(body, &rel); err != nil || rel.Tag == "" {
		return errors.New("the release information could not be read")
	}
	if !force && !newerVersion(rel.Tag, version) {
		fmt.Fprintf(os.Stderr, "xivlite %s is up to date (latest release %s).\n", version, rel.Tag)
		return nil
	}
	urls := map[string]string{}
	for _, a := range rel.Assets {
		urls[a.Name] = a.URL
	}
	if urls[releaseAsset] == "" || urls["SHA256SUMS"] == "" {
		return fmt.Errorf("release %s has no %s and SHA256SUMS", rel.Tag, releaseAsset)
	}
	archive, err := getBytes(c, urls[releaseAsset])
	if err != nil {
		return err
	}
	sums, err := getBytes(c, urls["SHA256SUMS"])
	if err != nil {
		return err
	}
	sum := sha256.Sum256(archive)
	if !hasChecksum(string(sums), releaseAsset, hex.EncodeToString(sum[:])) {
		return fmt.Errorf("%s does not match its SHA256SUMS entry; nothing was changed", releaseAsset)
	}
	files, err := untarFiles(archive, "xivlite", "xivlite-wrap")
	if err != nil {
		return err
	}
	dir, err := installDir()
	if err != nil {
		return err
	}
	// Each file is written beside its target and renamed over it, so a failure leaves the old one.
	for _, name := range []string{"xivlite-wrap", "xivlite"} {
		tmp := filepath.Join(dir, "."+name+".new")
		if err := os.WriteFile(tmp, files[name], 0o755); err != nil {
			return err
		}
		if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "Updated xivlite %s to %s in %s.\n", version, rel.Tag, dir)
	return nil
}

func hasChecksum(sums, name, want string) bool {
	sc := bufio.NewScanner(strings.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name && strings.EqualFold(f[0], want) {
			return true
		}
	}
	return false
}

// untarFiles extracts the named files from xivlite/ in a .tar.gz.
func untarFiles(archive []byte, names ...string) (map[string][]byte, error) {
	zr, err := gzip.NewReader(strings.NewReader(string(archive)))
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, n := range names {
		want["xivlite/"+n] = true
	}
	out := map[string][]byte{}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if want[h.Name] && h.Typeflag == tar.TypeReg {
			if out[filepath.Base(h.Name)], err = io.ReadAll(io.LimitReader(tr, 64<<20)); err != nil {
				return nil, err
			}
		}
	}
	for _, n := range names {
		if len(out[n]) == 0 {
			return nil, fmt.Errorf("the release archive has no xivlite/%s", n)
		}
	}
	return out, nil
}
