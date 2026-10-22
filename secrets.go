package main

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// Secrets in the config are sealed with AES-256-GCM under a random key kept in its own file,
// outside the config folder, so the config can be shared, backed up or pasted without them.
const sealedPrefix = "v1:"

func keyPath() string {
	if p := os.Getenv("XIVLITE_KEY"); p != "" {
		return p
	}
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "xivlite", "secret.key")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "xivlite", "secret.key")
}

func loadKey(create bool) ([]byte, error) {
	path := keyPath()
	key, err := os.ReadFile(path)
	if err == nil {
		if len(key) != 32 {
			return nil, fmt.Errorf("%s is not a xivlite key (expected 32 bytes)", path)
		}
		return key, nil
	}
	if !create || !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("cannot read the key that decrypts the saved password (%v); run: xivlite set-password", err)
	}
	key = make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(key); err != nil {
		f.Close()
		return nil, err
	}
	return key, f.Close()
}

func aead(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// seal binds the value to its setting name, so a sealed password cannot be moved to another setting.
func seal(key []byte, name, value string) (string, error) {
	g, err := aead(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := g.Seal(nonce, nonce, []byte(value), []byte(name))
	return sealedPrefix + base64.RawURLEncoding.EncodeToString(out), nil
}

func unseal(key []byte, name, sealed string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(sealed, sealedPrefix))
	if !strings.HasPrefix(sealed, sealedPrefix) || err != nil {
		return "", fmt.Errorf("%s is not a value written by xivlite", name)
	}
	g, err := aead(key)
	if err != nil {
		return "", err
	}
	if len(raw) < g.NonceSize() {
		return "", fmt.Errorf("%s is truncated", name)
	}
	plain, err := g.Open(nil, raw[:g.NonceSize()], raw[g.NonceSize():], []byte(name))
	if err != nil {
		return "", fmt.Errorf("%s cannot be decrypted with %s (was the key replaced?); run: xivlite set-password", name, keyPath())
	}
	return string(plain), nil
}

// commandSecret runs a password_command such as `secret-tool lookup ...` and returns its first line.
func commandSecret(command string) (string, error) {
	out, err := exec.Command("/bin/sh", "-c", command).Output()
	if err != nil {
		return "", fmt.Errorf("password_command failed: %v", err)
	}
	line, _, _ := strings.Cut(string(out), "\n")
	line = strings.TrimRight(line, "\r")
	if line == "" {
		return "", errors.New("password_command printed nothing")
	}
	return line, nil
}

// readSecret reads a line from the terminal with echo off, or from stdin when it is not a terminal.
func readSecret(prompt string) (string, error) {
	fd := os.Stdin.Fd()
	var saved syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCGETS, uintptr(unsafe.Pointer(&saved)))
	terminal := errno == 0
	if terminal {
		fmt.Fprint(os.Stderr, prompt)
		quiet := saved
		quiet.Lflag &^= syscall.ECHO
		syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&quiet)))
		restore := func() { syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&saved))) }
		interrupted := make(chan os.Signal, 1)
		signal.Notify(interrupted, os.Interrupt, syscall.SIGTERM)
		go func() {
			if _, ok := <-interrupted; ok {
				restore()
				fmt.Fprintln(os.Stderr)
				os.Exit(130)
			}
		}()
		defer func() {
			signal.Stop(interrupted)
			close(interrupted)
			restore()
			fmt.Fprintln(os.Stderr)
		}()
	}
	return readLine("")
}

// stdin is shared so successive prompts fed from a pipe each get their own line.
var stdin = bufio.NewReader(os.Stdin)

func readLine(prompt string) (string, error) {
	if prompt != "" {
		fmt.Fprint(os.Stderr, prompt)
	}
	line, err := stdin.ReadString('\n')
	if err != nil && line == "" {
		return "", errors.New("no input")
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// onTerminal is true only for a real terminal (/dev/null is a character device too).
func onTerminal() bool {
	var t syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, os.Stdin.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&t)))
	return errno == 0
}

// setConfigValue writes key = value in place of the first of replaced keys, drops the others, and
// keeps every other line (comments included).
func setConfigValue(path, key, value string, replaced ...string) error {
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	drop := map[string]bool{key: true}
	for _, r := range replaced {
		drop[r] = true
	}
	var lines []string
	written := false
	text := strings.TrimRight(string(old), "\n")
	if text != "" {
		for _, line := range strings.Split(text, "\n") {
			k, _, found := strings.Cut(strings.TrimSpace(line), "=")
			k = strings.ToLower(strings.TrimSpace(k))
			if found && !strings.HasPrefix(k, "#") && drop[k] {
				if !written {
					lines = append(lines, key+" = "+value)
					written = true
				}
				continue
			}
			lines = append(lines, line)
		}
	}
	if !written {
		lines = append(lines, key+" = "+value)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// rawConfig returns the config's settings (last one wins) without validating them.
func rawConfig(path string) map[string]string {
	values := map[string]string{}
	b, _ := os.ReadFile(path)
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		k, v, found := strings.Cut(line, "=")
		if found && !strings.HasPrefix(line, "#") {
			values[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
	}
	return values
}

func saveSealed(cfgPath, name, value string, replaced ...string) error {
	key, err := loadKey(true)
	if err != nil {
		return err
	}
	sealed, err := seal(key, name, value)
	if err != nil {
		return err
	}
	return setConfigValue(cfgPath, name, sealed, replaced...)
}

// savedUsername is the username already in the config, decrypted when needed ("" if none or unreadable).
func savedUsername(raw map[string]string) string {
	if raw["username_encrypted"] == "" {
		return raw["username"]
	}
	key, err := loadKey(false)
	if err != nil {
		return ""
	}
	name, _ := unseal(key, "username_encrypted", raw["username_encrypted"])
	return name
}

func askUsername(cfgPath, current string) error {
	prompt := "Square Enix ID (not the email address): "
	if current != "" {
		prompt = fmt.Sprintf("Square Enix ID [%s]: ", current)
	}
	name, err := readLine(prompt)
	if err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		if current != "" {
			return nil
		}
		return errors.New("no Square Enix ID entered")
	}
	return saveSealed(cfgPath, "username_encrypted", name, "username")
}

func askPassword(cfgPath string, saved bool) error {
	prompt := "Square Enix password: "
	if saved {
		prompt = "Square Enix password (Enter keeps the saved one): "
	}
	pw, err := readSecret(prompt)
	if err != nil {
		return err
	}
	if pw == "" {
		if saved {
			return nil
		}
		return errors.New("no password entered")
	}
	if onTerminal() {
		if again, _ := readSecret("Again, to confirm: "); again != pw {
			return errors.New("the passwords did not match; the password was not changed")
		}
	}
	return saveSealed(cfgPath, "password_encrypted", pw, "password", "password_file", "password_command")
}

func askOTPSecret(cfgPath string, saved, required bool) error {
	prompt := "Authenticator secret, base32 or otpauth:// URI (Enter if the account has none): "
	if saved {
		prompt = "Authenticator secret (Enter keeps the saved one): "
	}
	secret, err := readSecret(prompt)
	if err != nil {
		return err
	}
	if secret = strings.TrimSpace(secret); secret == "" {
		if required && !saved {
			return errors.New("no secret entered")
		}
		return nil
	}
	otp, err := otpKey(secret)
	if err != nil {
		return err
	}
	if err := saveSealed(cfgPath, "otp_secret_encrypted", secret, "otp_secret"); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Current one-time password: %s (it should match your authenticator app)\n", freshTOTP(otp))
	return nil
}

// askGamePath accepts the install folder, its game/ or boot/ folder, or an exe inside them.
// It returns the folder when the game is not there yet and should be installed into it.
func askGamePath(cfgPath, current string) (string, error) {
	prompt := "Game folder, holding boot/ and game/, or where to install the game (Enter to skip): "
	if current != "" {
		prompt = fmt.Sprintf("Game folder [%s]: ", current)
	}
	for tries := 0; tries < 3; tries++ {
		path, err := readLine(prompt)
		if err != nil {
			return "", err
		}
		if path = expandHome(strings.Trim(strings.TrimSpace(path), `"'`)); path == "" {
			return "", nil
		}
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		var problem error
		for _, root := range []string{path, installRootFromExe(filepath.Join(path, "x")), installRootFromExe(path)} {
			if root == "" {
				continue
			}
			if problem = (install{root: root}).validate(); problem == nil {
				return "", setConfigValue(cfgPath, "game_path", root)
			}
		}
		answer, err := readLine(fmt.Sprintf("The game is not in %s. Download and install it there (about 100 GB)? [y/N] ", path))
		if err != nil {
			return "", err
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(answer)), "y") {
			return path, setConfigValue(cfgPath, "game_path", path)
		}
		fmt.Fprintln(os.Stderr, problem)
	}
	return "", errors.New("no valid game folder entered")
}

func setUsername(cfgPath string) error {
	return askUsername(cfgPath, savedUsername(rawConfig(cfgPath)))
}

func setPassword(cfgPath string) error {
	return askPassword(cfgPath, false)
}

func setOTPSecret(cfgPath string) error {
	return askOTPSecret(cfgPath, false, true)
}

// setup asks for everything the config needs, so it never has to be edited by hand.
func setup(cfgPath string) error {
	if _, err := os.Stat(cfgPath); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
			return err
		}
		header := "# Written by `xivlite setup`; run it again to change these. config.example lists the other settings.\n"
		if err := os.WriteFile(cfgPath, []byte(header), 0o600); err != nil {
			return err
		}
	}
	raw := rawConfig(cfgPath)
	fmt.Fprintf(os.Stderr, "Setting up %s. Secrets are saved encrypted; press Enter to keep a saved value.\n", cfgPath)
	if err := askUsername(cfgPath, savedUsername(raw)); err != nil {
		return err
	}
	hasPassword := raw["password"] != "" || raw["password_file"] != "" || raw["password_encrypted"] != "" || raw["password_command"] != ""
	if err := askPassword(cfgPath, hasPassword); err != nil {
		return err
	}
	if err := askOTPSecret(cfgPath, raw["otp_secret"] != "" || raw["otp_secret_encrypted"] != "", false); err != nil {
		return err
	}
	installInto, err := askGamePath(cfgPath, raw["game_path"])
	if err != nil {
		return err
	}
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		return err
	}
	if installInto == "" {
		fmt.Fprintln(os.Stderr, "Saved. `xivlite update` logs in and installs any patches without starting the game.")
		return nil
	}
	fmt.Fprintln(os.Stderr, "Saved. Installing the game now; if it is interrupted, `xivlite install` picks up where it stopped.")
	g, err := prepareFolder(cfg, installInto, true)
	if err != nil {
		return err
	}
	_, err = syncGame(cfg, g, false)
	return err
}
