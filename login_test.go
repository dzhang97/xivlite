//go:build xivlite_mock

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeInstall lays out the files xivlite reads: version files, boot exes to hash, the game exe.
func fakeInstall(t *testing.T) string {
	root := t.TempDir()
	files := map[string]string{
		"boot/ffxivboot.ver": "2026.08.20.0000.0001", "boot/ffxivboot.bck": "2026.08.20.0000.0001",
		"boot/ffxivboot.exe": "a", "boot/ffxivboot64.exe": "bb", "boot/ffxivlauncher64.exe": "ccc", "boot/ffxivupdater64.exe": "dddd",
		"game/ffxiv_dx11.exe": "MZ", "game/ffxivgame.ver": "2026.10.01.0000.0000", "game/ffxivgame.bck": "2026.10.01.0000.0000",
	}
	for i := 1; i <= 5; i++ {
		v := "2026.09.01.0000.000" + strconv.Itoa(i)
		files["game/sqpack/ex"+strconv.Itoa(i)+"/ex"+strconv.Itoa(i)+".ver"] = v
		files["game/sqpack/ex"+strconv.Itoa(i)+"/ex"+strconv.Itoa(i)+".bck"] = v
	}
	for name, body := range files {
		p := filepath.Join(root, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func testConfig(root string) *config {
	return &config{username: "warrior", password: "hunter2", gamePath: root, lang: langEnglish,
		checkGate: true, onPatch: "install", acceptLanguage: "en-US"}
}

func checkMock(t *testing.T, m *mockSE) {
	t.Helper()
	for _, f := range m.failures {
		t.Error("mock server: " + f)
	}
}

func TestLoginPlainArgs(t *testing.T) {
	m := startMockSE()
	defer m.srv.Close()
	root := fakeInstall(t)
	cfg := testConfig(root)
	cfg.extraArgs = []gameArg{{"SYS.ScreenMode", "2"}}
	p, err := prepareLaunch(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	checkMock(t, m)
	want := "DEV.DataPathType=1 DEV.MaxEntitledExpansionID=5 DEV.TestSID=" + mockUID +
		" DEV.UseSqPack=1 SYS.Region=3 language=1 resetConfig=0 ver=2026.10.01.0000.0000 SYS.ScreenMode=2"
	if got := strings.Join(p.args, " "); got != want || p.patch || p.exe != filepath.Join(root, "game", "ffxiv_dx11.exe") {
		t.Errorf("plan %+v\nwant args %s", p, want)
	}
	if m.lastForm["sqexid"] != "warrior" || m.lastForm["otppw"] != "" {
		t.Errorf("form %v", m.lastForm)
	}
}

func TestLoginEncryptedArgsWithOTP(t *testing.T) {
	m := startMockSE()
	defer m.srv.Close()
	m.otpKey, _ = otpKey("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ")
	cfg := testConfig(fakeInstall(t))
	cfg.encryptArgs = true
	cfg.otpSecret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	cfg.lang = langJapanese
	before := tickCount()
	p, err := prepareLaunch(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	checkMock(t, m)
	if len(p.args) != 1 {
		t.Fatalf("args %v", p.args)
	}
	// The key is the tick count's high half; try the window we started in and the next one.
	var plain string
	for _, k := range []uint32{before, before + 0x10000} {
		if s, err := decryptArgs(p.args[0], k); err == nil && strings.HasPrefix(s, " /T =") {
			plain = s
			break
		}
	}
	if !strings.Contains(plain, " /DEV.TestSID ="+mockUID) || !strings.Contains(plain, " /language =0") {
		t.Errorf("decrypted %q", plain)
	}
}

func TestLoginFromSteamCommandExe(t *testing.T) {
	m := startMockSE()
	defer m.srv.Close()
	root := fakeInstall(t)
	cfg := testConfig("")
	cmd := []string{"/steam/reaper", "--", "/tools/proton", "waitforexitandrun", filepath.Join(root, "boot", "ffxivboot.exe")}
	p, err := prepareLaunch(cfg, exeIn(cmd))
	if err != nil {
		t.Fatal(err)
	}
	final := spliceCommand(cmd, p)
	if final[4] != filepath.Join(root, "game", "ffxiv_dx11.exe") || len(final) != 5+len(p.args) {
		t.Errorf("final %v", final)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	m := startMockSE()
	defer m.srv.Close()
	cfg := testConfig(fakeInstall(t))
	cfg.password = "wrong"
	_, err := prepareLaunch(cfg, "")
	if err == nil || !strings.Contains(err.Error(), "password is incorrect") {
		t.Fatalf("err %v", err)
	}
}

func TestHotfixPatchModes(t *testing.T) {
	for _, repo := range []string{"game", "boot"} {
		m := startMockSE()
		m.addHotfix(repo)
		root := fakeInstall(t)
		cfg := testConfig(root)

		cfg.onPatch = "fail"
		if _, err := prepareLaunch(cfg, ""); err == nil || !strings.Contains(err.Error(), "xivlite update") {
			t.Errorf("%s hotfix, on_patch=fail: %v", repo, err)
		}
		cfg.onPatch = "boot"
		p, err := prepareLaunch(cfg, "")
		if err != nil || !p.patch || p.exe != filepath.Join(root, "boot", "ffxivboot64.exe") || len(p.args) != 0 {
			t.Errorf("%s hotfix, on_patch=boot: plan %+v err %v", repo, p, err)
		}

		cfg.onPatch = "install"
		p, err = prepareLaunch(cfg, "")
		if err != nil || p.patch || len(p.args) != 8 {
			t.Fatalf("%s hotfix, on_patch=install: plan %+v err %v", repo, p, err)
		}
		checkMock(t, m)
		hot := m.patches[len(m.patches)-1]
		g := install{root: root}
		if got := g.ver(repo); got != hot.version {
			t.Errorf("%s version after hotfix: %s", repo, got)
		}
		bck, _ := os.ReadFile(strings.TrimSuffix(g.verFile(repo), ".ver") + ".bck")
		if string(bck) != hot.version {
			t.Errorf("%s .bck: %q", repo, bck)
		}
		exe := map[string]string{"game": "game/ffxiv_dx11.exe", "boot": "boot/ffxivboot64.exe"}[repo]
		if b, _ := os.ReadFile(filepath.Join(root, exe)); !strings.Contains(string(b), "hotfixed") {
			t.Errorf("%s was not patched", exe)
		}
		if repo == "game" && !strings.Contains(strings.Join(p.args, " "), "ver="+hot.version) {
			t.Errorf("launch arguments carry the old version: %v", p.args)
		}
		if _, err := os.Stat(filepath.Join(root, ".xivlite-patches")); err == nil {
			t.Error("patch folder left behind")
		}
		m.srv.Close()
	}
}

func TestFreshInstall(t *testing.T) {
	m := startMockSE()
	defer m.srv.Close()
	dir := t.TempDir()
	root := filepath.Join(dir, "FINAL FANTASY XIV")
	cfg := testConfig("")
	cfg.path = filepath.Join(dir, "config")
	os.WriteFile(cfg.path, []byte("# mine\n"), 0o600)

	g, err := prepareFolder(cfg, root, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := syncGame(cfg, g, false); err != nil {
		t.Fatal(err)
	}
	checkMock(t, m)
	if m.downloads != 7 {
		t.Errorf("%d downloads, want 7 (boot, game, ex1-5)", m.downloads)
	}
	if b, _ := os.ReadFile(cfg.path); !strings.Contains(string(b), "game_path = "+root) {
		t.Errorf("game_path not recorded:\n%s", b)
	}
	for repo, want := range map[string]string{"boot": "2026.08.20.0000.0001", "game": "2026.10.01.0000.0000", "ex3": "2026.09.01.0000.0003"} {
		if got := g.ver(repo); got != want {
			t.Errorf("%s version %s, want %s", repo, got, want)
		}
	}
	for _, f := range []string{"boot/ffxivconfig64.exe", "game/sqpack/ex5/0c0500.win32.dat0", "game/sqpack/ffxiv/0a0000.win32.dat0"} {
		if _, err := os.Stat(filepath.Join(root, f)); err != nil {
			t.Errorf("missing %s", f)
		}
	}
	// The freshly installed game launches, and nothing is downloaded again.
	p, err := prepareLaunch(cfg, "")
	if err != nil || p.patch || m.downloads != 7 {
		t.Errorf("launch after install: %+v %v (downloads %d)", p, err, m.downloads)
	}
}

func TestDownloadsRecover(t *testing.T) {
	m := startMockSE()
	defer m.srv.Close()
	m.addHotfix("game")
	root := fakeInstall(t)
	hot := m.patches[len(m.patches)-1]
	entries, err := parsePatchList(m.patchList(map[string]string{"game": "2026.10.01.0000.0000", "ex1": "9", "ex2": "9", "ex3": "9", "ex4": "9", "ex5": "9"}))
	if err != nil || len(entries) != 1 {
		t.Fatalf("list: %v %v", entries, err)
	}
	pt := newPatcher(install{root: root}, "")
	path := pt.file(entries[0])
	os.MkdirAll(filepath.Dir(path), 0o755)

	// A corrupt complete file is replaced; a partial download is resumed.
	os.WriteFile(path, bytes.Repeat([]byte{0}, len(hot.data)), 0o644)
	os.WriteFile(path+".part", hot.data[:len(hot.data)/2], 0o644)
	if err := pt.run(entries); err != nil {
		t.Fatal(err)
	}
	if m.downloads != 1 {
		t.Errorf("%d downloads", m.downloads)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "game", "ffxiv_dx11.exe")); !strings.Contains(string(b), "hotfixed") {
		t.Error("hotfix not applied")
	}
}

func TestParsePatchListRejectsOddURLs(t *testing.T) {
	if _, err := parsePatchList("10\t1\t1\t1\t2026.01.01.0000.0000\thttp://x/evil/../../p.patch"); err == nil {
		t.Error("odd URL accepted")
	}
	if e, _ := parsePatchList(""); len(e) != 0 {
		t.Error("empty list")
	}
}

func TestGateClosedAndDamagedVersion(t *testing.T) {
	m := startMockSE()
	defer m.srv.Close()
	m.gateClosed = true
	root := fakeInstall(t)
	if _, err := prepareLaunch(testConfig(root), ""); err == nil || !strings.Contains(err.Error(), "maintenance") {
		t.Errorf("closed gate: %v", err)
	}
	m.gateClosed = false
	os.WriteFile(filepath.Join(root, "game", "sqpack", "ex3", "ex3.bck"), []byte("2026\n.01"), 0o644)
	if _, err := prepareLaunch(testConfig(root), ""); err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Errorf("damaged version file: %v", err)
	}
}

func TestConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	os.WriteFile(filepath.Join(dir, "pw"), []byte("s3cret #x\n"), 0o600)
	os.WriteFile(path, []byte("# comment\nusername = warrior\npassword_file = "+filepath.Join(dir, "pw")+
		"\nlanguage = German\nencrypt_args = no\nextra_args = SYS.ScreenMode=2 UserPath=C:\\x\non_patch = fail\n"), 0o600)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.password != "s3cret #x" || cfg.lang != langGerman || cfg.encryptArgs || cfg.onPatch != "fail" || len(cfg.extraArgs) != 2 || !cfg.checkGate {
		t.Errorf("%+v", cfg)
	}
	os.WriteFile(path, []byte("username = a\npassword = b\nbogus = 1\n"), 0o600)
	if _, err := loadConfig(path); err == nil {
		t.Error("unknown key accepted")
	}
}

func TestSetupInstallsTheGame(t *testing.T) {
	m := startMockSE()
	defer m.srv.Close()
	dir := t.TempDir()
	t.Setenv("XIVLITE_KEY", filepath.Join(dir, "secret.key"))
	root := filepath.Join(dir, "New Game Folder")
	feed(t, "warrior", "hunter2", "", root, "y")
	if err := setup(filepath.Join(dir, "config")); err != nil {
		t.Fatal(err)
	}
	checkMock(t, m)
	if err := (install{root: root}).validate(); err != nil || m.downloads != 7 {
		t.Errorf("after setup: %v, %d downloads", err, m.downloads)
	}
}
