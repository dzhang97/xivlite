package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSealRoundTrip(t *testing.T) {
	t.Setenv("XIVLITE_KEY", filepath.Join(t.TempDir(), "k", "secret.key"))
	key, err := loadKey(true)
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(keyPath()); st.Mode().Perm() != 0o600 {
		t.Errorf("key mode %v", st.Mode())
	}
	again, err := loadKey(false)
	if err != nil || string(again) != string(key) {
		t.Fatal("key not reused")
	}
	sealed, err := seal(key, "password_encrypted", "p@ss word=#1")
	if err != nil || !strings.HasPrefix(sealed, sealedPrefix) || strings.Contains(sealed, "p@ss") {
		t.Fatalf("sealed %q %v", sealed, err)
	}
	if got, err := unseal(key, "password_encrypted", sealed); err != nil || got != "p@ss word=#1" {
		t.Errorf("unseal %q %v", got, err)
	}
	if _, err := unseal(key, "otp_secret_encrypted", sealed); err == nil {
		t.Error("sealed value moved to another setting was accepted")
	}
	other := make([]byte, 32)
	if _, err := unseal(other, "password_encrypted", sealed); err == nil {
		t.Error("wrong key accepted")
	}
}

func TestSetConfigValueKeepsOtherLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	os.WriteFile(path, []byte("# my notes\nusername = warrior\npassword = old\n# password = commented\npassword_file = ~/pw\nlanguage = french\n"), 0o644)
	if err := setConfigValue(path, "password_encrypted", "v1:abc", "password", "password_file"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	want := "# my notes\nusername = warrior\npassword_encrypted = v1:abc\n# password = commented\nlanguage = french\n"
	if string(b) != want {
		t.Errorf("got\n%s", b)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("config mode %v", st.Mode())
	}
}

func TestConfigSecretSources(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XIVLITE_KEY", filepath.Join(dir, "secret.key"))
	key, _ := loadKey(true)
	pw, _ := seal(key, "password_encrypted", "hunter2")
	otp, _ := seal(key, "otp_secret_encrypted", "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ")
	path := filepath.Join(dir, "config")
	write := func(body string) { os.WriteFile(path, []byte("username = warrior\n"+body), 0o600) }

	write("password_encrypted = " + pw + "\notp_secret_encrypted = " + otp + "\n")
	cfg, err := loadConfig(path)
	if err != nil || cfg.password != "hunter2" || cfg.otpSecret != "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" {
		t.Fatalf("encrypted: %+v %v", cfg, err)
	}

	write("password_command = printf 'from keyring\\nsecond line\\n'\n")
	if cfg, err := loadConfig(path); err != nil || cfg.password != "from keyring" {
		t.Errorf("command: %+v %v", cfg, err)
	}

	write("password = a\npassword_encrypted = " + pw + "\n")
	if _, err := loadConfig(path); err == nil || !strings.Contains(err.Error(), "only one") {
		t.Errorf("two sources: %v", err)
	}

	write("")
	if _, err := loadConfig(path); err == nil || !strings.Contains(err.Error(), "set-password") {
		t.Errorf("no source: %v", err)
	}

	os.Remove(keyPath())
	write("password_encrypted = " + pw + "\n")
	if _, err := loadConfig(path); err == nil || !strings.Contains(err.Error(), "set-password") {
		t.Errorf("missing key: %v", err)
	}
}
