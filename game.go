package main

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const baseGameVersion = "2012.01.01.0000.0000"

// bootFilesToHash are hashed into the version report the patch server checks for tampering.
var bootFilesToHash = []string{"ffxivboot.exe", "ffxivboot64.exe", "ffxivlauncher64.exe", "ffxivupdater64.exe"}

// gameExeNames are the executables a launch command may name; any of them identifies the install.
var gameExeNames = []string{"ffxiv_dx11.exe", "ffxivboot.exe", "ffxivboot64.exe", "ffxivlauncher64.exe", "ffxivlauncher.exe"}

func isGameExe(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	for _, n := range gameExeNames {
		if base == n {
			return true
		}
	}
	return false
}

// installRootFromExe maps <root>/game/x.exe or <root>/boot/x.exe to <root>.
func installRootFromExe(exe string) string {
	dir := filepath.Dir(exe)
	switch strings.ToLower(filepath.Base(dir)) {
	case "game", "boot":
		return filepath.Dir(dir)
	}
	return ""
}

type install struct{ root string }

func (g install) gameExe() string { return filepath.Join(g.root, "game", "ffxiv_dx11.exe") }
func (g install) bootExe() string { return filepath.Join(g.root, "boot", "ffxivboot64.exe") }

func (g install) validate() error {
	for _, p := range []string{g.gameExe(), filepath.Join(g.root, "boot")} {
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("%s is not a FINAL FANTASY XIV install (missing %s)", g.root, p)
		}
	}
	return nil
}

func (g install) verFile(repo string) string {
	switch repo {
	case "boot":
		return filepath.Join(g.root, "boot", "ffxivboot.ver")
	case "game":
		return filepath.Join(g.root, "game", "ffxivgame.ver")
	}
	return filepath.Join(g.root, "game", "sqpack", repo, repo+".ver")
}

// ver mirrors Repository.GetVer: a missing or blank file means the base version.
func (g install) ver(repo string) string {
	b, err := os.ReadFile(g.verFile(repo))
	if err != nil || strings.TrimSpace(string(b)) == "" {
		return baseGameVersion
	}
	return string(b)
}

func (g install) checkVersionSanity(maxExpansion int) error {
	repos := []string{"game"}
	for i := 1; i <= maxExpansion; i++ {
		repos = append(repos, fmt.Sprintf("ex%d", i))
	}
	for _, repo := range repos {
		for _, bck := range []bool{false, true} {
			path := g.verFile(repo)
			if bck {
				path = strings.TrimSuffix(path, ".ver") + ".bck"
			}
			b, _ := os.ReadFile(path)
			text := string(b)
			if strings.TrimSpace(text) == "" {
				text = baseGameVersion
			}
			if strings.Contains(text, "\n") || strings.Trim(text, "\x00") == "" {
				return fmt.Errorf("version file %s is damaged; repair the game files with the official launcher", path)
			}
		}
	}
	return nil
}

func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha1.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d/%s", n, hex.EncodeToString(h.Sum(nil))), nil
}

// versionReport is the body sent to patch-gamever: boot version and exe hashes, then one line per expansion.
func (g install) versionReport(maxExpansion int) (string, error) {
	var sb strings.Builder
	sb.WriteString(g.ver("boot") + "=")
	for i, name := range bootFilesToHash {
		h, err := fileHash(filepath.Join(g.root, "boot", name))
		if err != nil {
			return "", fmt.Errorf("boot files incomplete: %v", err)
		}
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(name + "/" + h)
	}
	sb.WriteString("\n")
	for i := 1; i <= maxExpansion; i++ {
		fmt.Fprintf(&sb, "ex%d\t%s\n", i, g.ver(fmt.Sprintf("ex%d", i)))
	}
	return sb.String(), nil
}
