//go:build xivlite_mock

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// samplePatches exercise every chunk and SQPK command xivlite applies. The same files, applied by
// XIVLauncher's C# ZiPatch code, produce the tree digest in testdata/zipatch_digest.txt.
func samplePatches() [][]byte {
	text := func(seed string, n int) []byte {
		var b bytes.Buffer
		for i := 0; b.Len() < n; i++ {
			fmt.Fprintf(&b, "%s line %d %x\n", seed, i, i*i*2654435761)
		}
		return b.Bytes()[:n]
	}
	blocks := func(fill byte, n int) []byte { return bytes.Repeat([]byte{fill}, n<<7) }

	p1 := newZiPatch()
	p1.targetInfo(0)
	p1.addFile("ffxiv_dx11.exe", 0, text("exe", 50000))
	p1.addFile("data/big.bin", 0, text("big-a", 40000))
	p1.addFile("data/big.bin", 40000, text("big-b", 20000))
	p1.dir("ADIR", "/movie/ffxiv")
	p1.dir("ADIR", "/sqpack/ffxiv")
	p1.dir("ADIR", "/sqpack/ex1")
	p1.dir("ADIR", "/sqpack/ex2")
	p1.addFile("movie/ffxiv/00000.bk2", 0, text("opening", 3000))
	p1.addFile("movie/ffxiv/00004.bk2", 0, text("other movie", 3000))
	p1.addFile("sqpack/ex1/keep.var", 0, text("var", 500))
	p1.addFile("sqpack/ex1/020100.win32.dat0", 0, text("old ex1", 4000))
	p1.header('D', 'V', 0x0a, 0x0000, 0, text("dat version header", 1024))
	p1.header('D', 'D', 0x0a, 0x0000, 0, text("dat data header", 1024))
	p1.header('I', 'V', 0x0a, 0x0000, 0, text("index version header", 1024))
	p1.header('I', 'I', 0x0a, 0x0000, 1, text("index2 header", 1024))
	p1.addData(0x0a, 0x0000, 0, 16, blocks(0xab, 3), 2)
	p1.emptyData('E', 0x0a, 0x0000, 0, 24, 4)

	p2 := newZiPatch()
	p2.targetInfo(0)
	p2.emptyData('D', 0x0a, 0x0000, 0, 16, 1)
	p2.fileOp('R', "", 0, 1, nil)
	p2.fileOp('D', "movie/ffxiv/00004.bk2", 0, 0, nil)
	p2.dir("ADIR", "/scratch")
	p2.dir("DELD", "/scratch")
	p2.addFile("ffxiv_dx11.exe", 0, text("new exe", 7000))
	p2.header('I', 'V', 0x0c, 0x0100, 0, text("ex1 index", 1024))
	p2.addData(0x0c, 0x0200, 1, 2, blocks(0x5a, 2), 0)
	return [][]byte{p1.bytes(), p2.bytes()}
}

func treeDigest(root string) (string, error) {
	var lines []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			lines = append(lines, "dir "+filepath.ToSlash(rel))
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		lines = append(lines, fmt.Sprintf("file %s %d %s", filepath.ToSlash(rel), len(b), hex.EncodeToString(sum[:])))
		return nil
	})
	sort.Strings(lines)
	return strings.Join(lines, "\n") + "\n", err
}

func TestZiPatchMatchesXIVLauncher(t *testing.T) {
	dir := t.TempDir()
	game := filepath.Join(dir, "game")
	for i, p := range samplePatches() {
		path := filepath.Join(dir, fmt.Sprintf("p%d.patch", i+1))
		os.WriteFile(path, p, 0o644)
		if out := os.Getenv("XIVLITE_DUMP_PATCHES"); out != "" {
			os.WriteFile(filepath.Join(out, filepath.Base(path)), p, 0o644)
		}
		if err := checkPatchCRC(path); err != nil {
			t.Fatalf("patch %d CRC: %v", i+1, err)
		}
		if err := applyPatchFile(path, game, true); err != nil {
			t.Fatalf("patch %d: %v", i+1, err)
		}
	}
	got, err := treeDigest(game)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/zipatch_digest.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("tree differs from XIVLauncher's result\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestZiPatchRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	evil := newZiPatch()
	evil.addFile("../escape.txt", 0, []byte("x"))
	path := filepath.Join(dir, "evil.patch")
	os.WriteFile(path, evil.bytes(), 0o644)
	if err := applyPatchFile(path, filepath.Join(dir, "game"), false); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Errorf("path escape: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "escape.txt")); err == nil {
		t.Error("file written outside the game folder")
	}

	good := samplePatches()[0]
	bad := append([]byte(nil), good...)
	bad[len(bad)/2] ^= 0xff
	os.WriteFile(path, bad, 0o644)
	if err := checkPatchCRC(path); err == nil {
		t.Error("corrupted patch passed the CRC check")
	}
	os.WriteFile(path, good[:len(good)-30], 0o644)
	if err := applyPatchFile(path, filepath.Join(dir, "g2"), false); err == nil {
		t.Error("truncated patch applied")
	}
}
