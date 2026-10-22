//go:build xivlite_mock

package main

import (
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
		checkGate: true, onPatch: "boot", acceptLanguage: "en-US"}
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

func TestPatchFallbacks(t *testing.T) {
	for _, mode := range []string{"game", "boot"} {
		m := startMockSE()
		m.gamePatch, m.bootPatch = mode == "game", mode == "boot"
		root := fakeInstall(t)
		cfg := testConfig(root)
		p, err := prepareLaunch(cfg, "")
		if err != nil || !p.patch || p.exe != filepath.Join(root, "boot", "ffxivboot64.exe") || len(p.args) != 0 {
			t.Errorf("%s patch: plan %+v err %v", mode, p, err)
		}
		cfg.onPatch = "fail"
		if _, err := prepareLaunch(cfg, ""); err == nil {
			t.Errorf("%s patch with on_patch=fail did not fail", mode)
		}
		m.srv.Close()
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
