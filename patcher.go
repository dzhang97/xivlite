package main

// Downloads Square Enix's patches, verifies them and applies them in order (port of XIVLauncher's
// PatchManager and RemotePatchInstaller).

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type patchEntry struct {
	length    int64
	version   string
	hashType  string
	blockSize int64
	hashes    []string
	url       string
}

var (
	patchURLPath = regexp.MustCompile(`/((game|boot)/.*)$`)
	patchExRepo  = regexp.MustCompile(`/(ex\d+)/`)
)

// repo is the version file the patch updates: boot, game or exN.
func (e patchEntry) repo() string {
	if strings.Contains(e.url, "/boot/") {
		return "boot"
	}
	if m := patchExRepo.FindStringSubmatch(e.url); m != nil {
		return m[1]
	}
	return "game"
}

func (e patchEntry) name() string { return e.repo() + "/" + e.version }

// parsePatchList reads the tab-separated rows of a patch list (a multipart body). Game rows have
// nine fields with SHA-1 block hashes; boot rows have six and no hashes.
func parsePatchList(body string) ([]patchEntry, error) {
	var out []patchEntry
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r", ""), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 6 && len(f) != 9 {
			continue
		}
		length, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil {
			continue
		}
		e := patchEntry{length: length, version: f[4], url: f[len(f)-1]}
		if len(f) == 9 {
			e.hashType = f[5]
			if e.blockSize, err = strconv.ParseInt(f[6], 10, 64); err != nil || e.blockSize <= 0 {
				return nil, fmt.Errorf("patch list: bad hash block size %q", f[6])
			}
			e.hashes = strings.Split(f[7], ",")
		}
		if !patchURLPath.MatchString(e.url) || strings.ContainsAny(e.version, "/\\") {
			return nil, fmt.Errorf("patch list: unexpected patch URL %q", e.url)
		}
		out = append(out, e)
	}
	if len(out) == 0 && strings.TrimSpace(body) != "" {
		return nil, errors.New("patch list has no patches in it")
	}
	return out, nil
}

func freeSpace(path string) (int64, error) {
	var st syscall.Statfs_t
	for p := path; ; p = filepath.Dir(p) {
		if err := syscall.Statfs(p, &st); err == nil {
			return int64(st.Bavail) * int64(st.Bsize), nil
		} else if p == filepath.Dir(p) {
			return 0, err
		}
	}
}

func mb(n int64) string { return fmt.Sprintf("%.1f MB", float64(n)/1e6) }

type patcher struct {
	g        install
	dir      string // where downloads are kept until installed
	client   *http.Client
	ahead    int // patches downloaded ahead of the one being installed
	workers  int
	progress bool

	done, total atomic.Int64
}

func newPatcher(g install, dir string) *patcher {
	if dir == "" {
		dir = filepath.Join(g.root, ".xivlite-patches")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if tlsConfig != nil {
		transport.TLSClientConfig = tlsConfig
	}
	return &patcher{g: g, dir: dir, client: &http.Client{Transport: transport}, ahead: 3, workers: 2, progress: onTerminal()}
}

func (p *patcher) file(e patchEntry) string {
	return filepath.Join(p.dir, filepath.FromSlash(patchURLPath.FindStringSubmatch(e.url)[1]))
}

func (p *patcher) checkSpace(entries []patchEntry) error {
	var all, largest int64
	for _, e := range entries {
		all += e.length
		largest = max(largest, e.length)
	}
	if os.Getenv("XIVLITE_IGNORE_SPACE") != "" {
		return nil
	}
	// As XIVLauncher: room for the installed result, and for the downloads waiting their turn.
	need := all + int64(p.ahead)*largest
	if err := os.MkdirAll(p.g.root, 0o755); err != nil {
		return err
	}
	free, err := freeSpace(p.g.root)
	if err != nil {
		return nil
	}
	if free < need {
		return fmt.Errorf("not enough free space: the update needs about %s, %s is free (XIVLITE_IGNORE_SPACE=1 skips this check)", mb(need), mb(free))
	}
	return nil
}

// verify checks a downloaded patch: SHA-1 per block for game patches, chunk CRCs for boot patches.
func verifyPatch(e patchEntry, path string) error {
	if e.hashType != "sha1" {
		if e.repo() != "boot" {
			return fmt.Errorf("%s: unknown hash type %q", e.name(), e.hashType)
		}
		return checkPatchCRC(path)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || st.Size() != e.length {
		return fmt.Errorf("%s: wrong size", e.name())
	}
	block := make([]byte, e.blockSize)
	for i := 0; ; i++ {
		n, err := io.ReadFull(f, block)
		if n == 0 && (err == io.EOF || err == io.ErrUnexpectedEOF) {
			if i != len(e.hashes) {
				return fmt.Errorf("%s: %d hash blocks, list has %d", e.name(), i, len(e.hashes))
			}
			return nil
		}
		if err != nil && err != io.ErrUnexpectedEOF {
			return err
		}
		sum := sha1.Sum(block[:n])
		if i >= len(e.hashes) || hex.EncodeToString(sum[:]) != e.hashes[i] {
			return fmt.Errorf("%s: block %d does not match its hash", e.name(), i)
		}
	}
}

// download fetches a patch, resuming a partial one, unless a verified copy is already there.
func (p *patcher) download(ctx context.Context, e patchEntry) error {
	path := p.file(e)
	if verifyPatch(e, path) == nil {
		p.done.Add(e.length)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	part := path + ".part"
	var lastErr error
	for attempt := 1; attempt <= 4; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * time.Second):
			}
		}
		lastErr = p.fetch(ctx, e, part)
		if lastErr == nil {
			if err := os.Rename(part, path); err != nil {
				return err
			}
			if err := verifyPatch(e, path); err != nil {
				os.Remove(path)
				lastErr = err
				continue
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		logf("%s: download failed (%v), attempt %d of 4", e.name(), lastErr, attempt)
	}
	return lastErr
}

func (p *patcher) fetch(ctx context.Context, e patchEntry, part string) error {
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	have, _ := f.Seek(0, io.SeekEnd)
	if e.length > 0 && have > e.length {
		f.Truncate(0)
		have, _ = f.Seek(0, io.SeekStart)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, e.url, nil)
	req.Header.Set("User-Agent", patcherUserAgent)
	if have > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusPartialContent && have > 0:
	case resp.StatusCode == http.StatusOK:
		if err := f.Truncate(0); err != nil {
			return err
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		have = 0
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && have == e.length:
		p.done.Add(have)
		return nil
	default:
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	// Progress counts this attempt's bytes; a failed attempt takes them back.
	counted := have
	p.done.Add(have)
	buf := make([]byte, 1<<20)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				p.done.Add(-counted)
				return err
			}
			p.done.Add(int64(n))
			counted += int64(n)
		}
		if rerr == io.EOF {
			return f.Sync()
		}
		if rerr != nil {
			p.done.Add(-counted)
			return rerr
		}
	}
}

func (p *patcher) setVer(repo, version string) error {
	path := p.g.verFile(repo)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(version), 0o644)
}

// verToBck copies each repository's .ver to its .bck once patching is done, as XIVLauncher does.
func (p *patcher) verToBck() error {
	repos := []string{"boot", "game"}
	for i := 1; i <= 20; i++ {
		repos = append(repos, fmt.Sprintf("ex%d", i))
	}
	for _, repo := range repos {
		b, err := os.ReadFile(p.g.verFile(repo))
		if err != nil {
			continue
		}
		if err := os.WriteFile(strings.TrimSuffix(p.g.verFile(repo), ".ver")+".bck", b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (p *patcher) report(stop <-chan struct{}, current *atomic.Value) {
	if !p.progress {
		return
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	last, lastAt := p.done.Load(), time.Now()
	for {
		select {
		case <-stop:
			fmt.Fprint(os.Stderr, "\r\033[K")
			return
		case now := <-tick.C:
			d := p.done.Load()
			speed := float64(d-last) / now.Sub(lastAt).Seconds() / 1e6
			last, lastAt = d, now
			fmt.Fprintf(os.Stderr, "\r\033[K%s / %s downloaded, %.1f MB/s - %s", mb(d), mb(p.total.Load()), speed, current.Load())
		}
	}
}

// run downloads and installs the patches in list order. Downloads run ahead of installation in
// p.workers parallel streams, at most p.ahead patches ahead.
func (p *patcher) run(entries []patchEntry) error {
	if len(entries) == 0 {
		return nil
	}
	if err := p.checkSpace(entries); err != nil {
		return err
	}
	for _, e := range entries {
		p.total.Add(e.length)
	}
	logf("installing %d patch(es), %s to download", len(entries), mb(p.total.Load()))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make([]chan error, len(entries))
	for i := range results {
		results[i] = make(chan error, 1)
	}
	slots := make(chan struct{}, p.ahead)
	var next atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < p.workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				// Take a slot before an index, so slots always go to the earliest patches.
				select {
				case slots <- struct{}{}:
				case <-ctx.Done():
					return
				}
				i := int(next.Add(1) - 1)
				if i >= len(entries) {
					<-slots
					return
				}
				results[i] <- p.download(ctx, entries[i])
			}
		}()
	}

	var current atomic.Value
	current.Store("starting")
	stop := make(chan struct{})
	go p.report(stop, &current)
	defer func() { close(stop); cancel(); wg.Wait() }()

	for i, e := range entries {
		current.Store(fmt.Sprintf("patch %d of %d", i+1, len(entries)))
		if err := <-results[i]; err != nil {
			return fmt.Errorf("downloading %s: %v", e.name(), err)
		}
		current.Store(fmt.Sprintf("installing %d of %d (%s)", i+1, len(entries), e.name()))
		target := filepath.Join(p.g.root, "game")
		if e.repo() == "boot" {
			target = filepath.Join(p.g.root, "boot")
		}
		started := time.Now()
		if err := applyPatchFile(p.file(e), target, false); err != nil {
			return fmt.Errorf("installing %s: %v (run the update again to retry)", e.name(), err)
		}
		if err := p.setVer(e.repo(), e.version); err != nil {
			return err
		}
		os.Remove(p.file(e))
		<-slots
		logf("installed %s (%d of %d, %s)", e.name(), i+1, len(entries), time.Since(started).Round(time.Second))
	}
	removeEmptyDirs(p.dir)
	return p.verToBck()
}

func removeEmptyDirs(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			removeEmptyDirs(filepath.Join(dir, e.Name()))
		}
	}
	os.Remove(dir)
}
