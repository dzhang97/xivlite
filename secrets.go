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
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", errors.New("no input")
	}
	return strings.TrimRight(line, "\r\n"), nil
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

func setPassword(cfgPath string) error {
	pw, err := readSecret("Square Enix password: ")
	if err != nil || pw == "" {
		return errors.New("no password entered")
	}
	if st, err := os.Stdin.Stat(); err == nil && st.Mode()&os.ModeCharDevice != 0 {
		again, _ := readSecret("Again, to confirm: ")
		if again != pw {
			return errors.New("the passwords did not match; nothing was changed")
		}
	}
	key, err := loadKey(true)
	if err != nil {
		return err
	}
	sealed, err := seal(key, "password_encrypted", pw)
	if err != nil {
		return err
	}
	if err := setConfigValue(cfgPath, "password_encrypted", sealed, "password", "password_file", "password_command"); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Saved the password encrypted in %s (key: %s).\n", cfgPath, keyPath())
	return nil
}

func setOTPSecret(cfgPath string) error {
	secret, err := readSecret("Authenticator secret (base32 or otpauth:// URI): ")
	if err != nil || secret == "" {
		return errors.New("no secret entered")
	}
	otp, err := otpKey(secret)
	if err != nil {
		return err
	}
	key, err := loadKey(true)
	if err != nil {
		return err
	}
	sealed, err := seal(key, "otp_secret_encrypted", secret)
	if err != nil {
		return err
	}
	if err := setConfigValue(cfgPath, "otp_secret_encrypted", sealed, "otp_secret"); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Saved the authenticator secret encrypted in %s. Current code: %s (compare it with your authenticator app).\n",
		cfgPath, freshTOTP(otp))
	return nil
}
