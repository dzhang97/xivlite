// xivlite: a minimal FINAL FANTASY XIV auto-login launcher for x86_64 Linux.
//
// It logs a Square Enix (non-Steam) account in, registers the session with the patch server and
// produces the game's launch arguments. Starting the game is left to whatever already runs Windows
// programs (Proton, Wine, DroidDeck): xivlite either wraps that command or hands the plan to a script.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var version = "1.1.0"

const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
	exitPatch = 3
)

const usage = `xivlite %s - FINAL FANTASY XIV auto-login launcher (Square Enix accounts, no Steam)

usage: xivlite [-c CONFIG] COMMAND

commands:
  exec [--] CMD...        log in, then exec CMD with the game and its arguments. If CMD names
                          ffxiv_dx11.exe or a boot/launcher exe (Steam's %%command%%), that word is
                          replaced; otherwise the game exe is appended (e.g. "exec -- wine").
  login [--exe EXE] [--out FILE]
                          log in and write the launch plan ("exe PATH" then one "arg VALUE" line
                          per argument) to FILE or stdout. EXE locates the install when the
                          config has no game_path.
  install [DIR]           download and install the game into DIR (or game_path), then exit
  update                  log in and install any game patches, without starting the game
  check [--exe EXE]       validate the config and game install without logging in
  setup                   ask for the account, password, authenticator secret and game folder,
                          and write the config (secrets encrypted); Enter keeps a saved value
  set-username, set-password, set-otp-secret
                          change just one of them
  otp                     print the current one-time password
  self-update [--force]   replace xivlite and xivlite-wrap with the newest release
  version

Patches are installed before the game starts (on_patch = install). Exit status: 0 launch the game,
3 the plan starts the official launcher instead (on_patch = boot), 1 error, 2 usage.

config: $XIVLITE_CONFIG, else $XDG_CONFIG_HOME/xivlite/config, else ~/.config/xivlite/config
log:    ~/.local/state/xivlite/xivlite.log
`

type config struct {
	path           string
	username       string
	password       string
	otpSecret      string
	gamePath       string
	lang           language
	freeTrial      bool
	encryptArgs    bool
	checkGate      bool
	onPatch        string
	patchDir       string
	acceptLanguage string
	extraArgs      []gameArg
}

func defaultConfigPath() string {
	if p := os.Getenv("XIVLITE_CONFIG"); p != "" {
		return p
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "xivlite", "config")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "xivlite", "config")
}

func parseBool(key, v string) (bool, error) {
	switch strings.ToLower(v) {
	case "1", "yes", "true", "on":
		return true, nil
	case "0", "no", "false", "off":
		return false, nil
	}
	return false, fmt.Errorf("%s: expected yes or no, got %q", key, v)
}

func loadConfig(path string) (*config, error) {
	cfg := &config{path: path, lang: langEnglish, encryptArgs: true, checkGate: true, onPatch: "install", acceptLanguage: "en-US"}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read config: %v (see config.example)", err)
	}
	defer f.Close()
	sources := map[string]string{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			return nil, fmt.Errorf("%s:%d: expected key = value", path, n)
		}
		key, value = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' && value[len(value)-1] == '"' || value[0] == '\'' && value[len(value)-1] == '\'') {
			value = value[1 : len(value)-1]
		}
		switch key {
		case "username", "username_encrypted", "password", "password_file", "password_encrypted", "password_command", "otp_secret", "otp_secret_encrypted":
			sources[key] = value
		case "game_path":
			cfg.gamePath = expandHome(value)
		case "language":
			l, ok := map[string]language{"ja": langJapanese, "japanese": langJapanese, "en": langEnglish, "english": langEnglish,
				"de": langGerman, "german": langGerman, "fr": langFrench, "french": langFrench}[strings.ToLower(value)]
			if !ok {
				return nil, fmt.Errorf("%s:%d: language must be japanese, english, german or french", path, n)
			}
			cfg.lang = l
		case "free_trial", "encrypt_args", "check_gate":
			b, err := parseBool(key, value)
			if err != nil {
				return nil, fmt.Errorf("%s:%d: %v", path, n, err)
			}
			*map[string]*bool{"free_trial": &cfg.freeTrial, "encrypt_args": &cfg.encryptArgs, "check_gate": &cfg.checkGate}[key] = b
		case "on_patch":
			if value != "install" && value != "boot" && value != "fail" {
				return nil, fmt.Errorf("%s:%d: on_patch must be install, boot or fail", path, n)
			}
			cfg.onPatch = value
		case "patch_dir":
			cfg.patchDir = expandHome(value)
		case "accept_language":
			cfg.acceptLanguage = value
		case "extra_args":
			for _, word := range strings.Fields(value) {
				k, v, ok := strings.Cut(word, "=")
				if !ok || k == "" {
					return nil, fmt.Errorf("%s:%d: extra_args takes key=value words, got %q", path, n, word)
				}
				cfg.extraArgs = append(cfg.extraArgs, gameArg{k, v})
			}
		default:
			return nil, fmt.Errorf("%s:%d: unknown setting %q", path, n, key)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if err := cfg.resolveSecrets(sources); err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	if st, err := os.Stat(path); err == nil && st.Mode().Perm()&0o077 != 0 {
		logf("warning: %s is readable by other users; run: chmod 600 %s", path, path)
	}
	return cfg, nil
}

// resolveSecrets takes the username from username or username_encrypted, the password from exactly
// one of its four settings, and the OTP secret from otp_secret or otp_secret_encrypted.
func (cfg *config) resolveSecrets(sources map[string]string) error {
	if sources["username"] != "" && sources["username_encrypted"] != "" {
		return errors.New("set only one of username, username_encrypted")
	}
	if sources["username"] == "" && sources["username_encrypted"] == "" {
		return errors.New("no username: run `xivlite setup`")
	}
	var given []string
	for _, k := range []string{"password", "password_file", "password_encrypted", "password_command"} {
		if sources[k] != "" {
			given = append(given, k)
		}
	}
	if len(given) == 0 {
		return errors.New("no password: run `xivlite setup`, or set password_command")
	}
	if len(given) > 1 {
		return fmt.Errorf("set only one of %s", strings.Join(given, ", "))
	}
	if sources["otp_secret"] != "" && sources["otp_secret_encrypted"] != "" {
		return errors.New("set only one of otp_secret, otp_secret_encrypted")
	}
	var key []byte
	needKey := sources["username_encrypted"] != "" || sources["password_encrypted"] != "" || sources["otp_secret_encrypted"] != ""
	if needKey {
		var err error
		if key, err = loadKey(false); err != nil {
			return err
		}
	}
	var err error
	cfg.username = sources["username"]
	if s := sources["username_encrypted"]; s != "" {
		if cfg.username, err = unseal(key, "username_encrypted", s); err != nil {
			return err
		}
	}
	switch given[0] {
	case "password":
		cfg.password = sources["password"]
		logf("warning: the password is stored in plain text; run `xivlite set-password` to encrypt it")
	case "password_file":
		var b []byte
		if b, err = os.ReadFile(expandHome(sources["password_file"])); err == nil {
			cfg.password = strings.TrimRight(string(b), "\r\n")
		}
		logf("warning: password_file holds the password in plain text; run `xivlite set-password` to encrypt it")
	case "password_encrypted":
		cfg.password, err = unseal(key, "password_encrypted", sources["password_encrypted"])
	case "password_command":
		cfg.password, err = commandSecret(sources["password_command"])
	}
	if err != nil {
		return err
	}
	if cfg.password == "" {
		return fmt.Errorf("%s is empty", given[0])
	}
	cfg.otpSecret = sources["otp_secret"]
	if s := sources["otp_secret_encrypted"]; s != "" {
		cfg.otpSecret, err = unseal(key, "otp_secret_encrypted", s)
	}
	return err
}

// maskName keeps the log useful without writing the account name into it.
func maskName(name string) string {
	if len(name) <= 2 {
		return "***"
	}
	return name[:2] + strings.Repeat("*", len(name)-2)
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[1:])
	}
	return p
}

var logFile io.Writer

func openLog() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	dir := filepath.Join(home, ".local", "state", "xivlite")
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	path := filepath.Join(dir, "xivlite.log")
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if st, err := os.Stat(path); err == nil && st.Size() > 512<<10 {
		flags |= os.O_TRUNC
	}
	if f, err := os.OpenFile(path, flags, 0o600); err == nil {
		logFile = f
	}
}

// logf never receives secrets: passwords, OTP codes and session ids are not passed to it.
func logf(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	fmt.Fprintln(os.Stderr, "xivlite: "+msg)
	if logFile != nil {
		fmt.Fprintf(logFile, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), msg)
	}
}

type plan struct {
	patch bool
	exe   string
	args  []string
}

func resolveInstall(cfg *config, exeHint string) (install, error) {
	root := cfg.gamePath
	if root == "" && exeHint != "" {
		root = installRootFromExe(exeHint)
	}
	if root == "" {
		return install{}, errors.New("game_path is not set and no game exe was given to locate the install")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return install{}, err
	}
	g := install{root: abs}
	return g, g.validate()
}

// prepareFolder readies a game folder for install/update. A new install gets empty boot/ and game/
// folders (the boot patches fill them) and is recorded as game_path.
func prepareFolder(cfg *config, root string, create bool) (install, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return install{}, err
	}
	g := install{root: abs}
	if g.validate() == nil {
		return g, nil
	}
	if !create {
		return g, g.validate()
	}
	for _, d := range []string{"boot", "game"} {
		if err := os.MkdirAll(filepath.Join(abs, d), 0o755); err != nil {
			return g, err
		}
	}
	if cfg.gamePath != abs {
		if err := setConfigValue(cfg.path, "game_path", abs); err != nil {
			return g, err
		}
		cfg.gamePath = abs
	}
	logf("installing FINAL FANTASY XIV into %s", abs)
	return g, nil
}

func otpCode(cfg *config) (string, error) {
	if cfg.otpSecret != "" {
		key, err := otpKey(cfg.otpSecret)
		if err != nil {
			return "", err
		}
		return freshTOTP(key), nil
	}
	if code := os.Getenv("XIVLITE_OTP"); code != "" {
		return strings.TrimSpace(code), nil
	}
	if onTerminal() {
		fmt.Fprint(os.Stderr, "One-time password (Enter if none): ")
		line, _ := stdin.ReadString('\n')
		return strings.TrimSpace(line), nil
	}
	return "", nil
}

func patchPlan(cfg *config, g install, why error) (*plan, error) {
	if cfg.onPatch != "boot" {
		return nil, fmt.Errorf("%v; run `xivlite update` (or set on_patch = install)", why)
	}
	exe := g.bootExe()
	if _, err := os.Stat(exe); err != nil {
		exe = filepath.Join(g.root, "boot", "ffxivboot.exe")
	}
	logf("%v: starting the official launcher to patch it; log in there once, then use xivlite again", why)
	return &plan{patch: true, exe: exe}, nil
}

func prepareLaunch(cfg *config, exeHint string) (*plan, error) {
	g, err := resolveInstall(cfg, exeHint)
	if err != nil {
		return nil, err
	}
	return syncGame(cfg, g, true)
}

// patchesNeeded installs pending patches (on_patch = install, or always when not launching), or
// hands over to the official launcher or stops, as on_patch says.
func patchesNeeded(cfg *config, g install, launch bool, what string, patches []patchEntry) (*plan, error) {
	if !launch || cfg.onPatch == "install" {
		return nil, newPatcher(g, cfg.patchDir).run(patches)
	}
	return patchPlan(cfg, g, fmt.Errorf("%s needs %d patch(es)", what, len(patches)))
}

// syncGame checks the login gate, brings boot/ up to date, logs in, registers the session and
// brings the game up to date. With launch it returns the launch plan.
func syncGame(cfg *config, g install, launch bool) (*plan, error) {
	logf("install %s, game version %s", g.root, g.ver("game"))
	s := newSession(cfg.acceptLanguage, cfg.lang)

	if cfg.checkGate {
		open, err := s.gateOpen()
		switch {
		case err != nil:
			logf("warning: could not read the login gate status (%v); trying anyway", err)
		case !open:
			return nil, errors.New("the login servers are closed (maintenance?)")
		}
	}

	boot, err := s.bootPatches(g)
	if err != nil {
		return nil, fmt.Errorf("could not check the boot version: %v", err)
	}
	if len(boot) > 0 {
		if p, err := patchesNeeded(cfg, g, launch, "the boot launcher", boot); p != nil || err != nil {
			return p, err
		}
	}

	otp, err := otpCode(cfg)
	if err != nil {
		return nil, err
	}
	logf("logging in as %s%s", maskName(cfg.username), map[bool]string{true: " with a one-time password", false: ""}[otp != ""])
	o, err := s.oauthLogin(cfg.username, cfg.password, otp, cfg.freeTrial)
	if err != nil {
		return nil, err
	}
	if !o.playable {
		return nil, errors.New("this account has no active subscription or service account")
	}
	if !o.termsAccepted {
		return nil, errors.New("this account has not accepted the current terms; accept them once in the official launcher")
	}
	logf("logged in: region %d, expansion level %d", o.region, o.maxExpansion)

	uid, patches, err := s.registerSession(g, o)
	if err != nil {
		return nil, err
	}
	if len(patches) > 0 {
		if p, err := patchesNeeded(cfg, g, launch, "the game", patches); p != nil || err != nil {
			return p, err
		}
	}
	if !launch {
		logf("the game is up to date (version %s)", g.ver("game"))
		return nil, nil
	}

	args := []gameArg{
		{"DEV.DataPathType", "1"},
		{"DEV.MaxEntitledExpansionID", strconv.Itoa(o.maxExpansion)},
		{"DEV.TestSID", uid},
		{"DEV.UseSqPack", "1"},
		{"SYS.Region", strconv.Itoa(o.region)},
		{"language", strconv.Itoa(int(cfg.lang))},
		{"resetConfig", "0"},
		{"ver", g.ver("game")},
	}
	args = append(args, cfg.extraArgs...)
	p := &plan{exe: g.gameExe()}
	if cfg.encryptArgs {
		p.args = []string{encryptedArgs(args, tickCount())}
	} else {
		p.args = plainArgs(args)
	}
	logf("session ready")
	return p, nil
}

func writePlan(w io.Writer, p *plan) error {
	if strings.ContainsAny(p.exe, "\n\r") {
		return errors.New("game path contains a newline")
	}
	status := "ok"
	if p.patch {
		status = "patch"
	}
	fmt.Fprintf(w, "status %s\nexe %s\n", status, p.exe)
	for _, a := range p.args {
		if strings.ContainsAny(a, "\n\r") {
			return errors.New("an argument contains a newline")
		}
		fmt.Fprintf(w, "arg %s\n", a)
	}
	return nil
}

// spliceCommand puts the plan's exe and arguments where cmd names a game exe, or appends them.
func spliceCommand(cmd []string, p *plan) []string {
	out := make([]string, 0, len(cmd)+len(p.args)+1)
	for i, word := range cmd {
		if isGameExe(word) {
			out = append(append(append(out, cmd[:i]...), p.exe), p.args...)
			return append(out, cmd[i+1:]...)
		}
	}
	return append(append(append(out, cmd...), p.exe), p.args...)
}

func exeIn(cmd []string) string {
	for _, word := range cmd {
		if isGameExe(word) {
			return word
		}
	}
	return ""
}

func run(argv []string) int {
	cfgPath := defaultConfigPath()
	if len(argv) >= 2 && argv[0] == "-c" {
		cfgPath, argv = argv[1], argv[2:]
	}
	if len(argv) == 0 {
		fmt.Fprintf(os.Stderr, usage, version)
		return exitUsage
	}
	cmd, rest := argv[0], argv[1:]
	switch cmd {
	case "help", "-h", "--help":
		fmt.Printf(usage, version)
		return exitOK
	case "version", "--version":
		fmt.Println("xivlite", version)
		return exitOK
	}

	openLog()
	switch cmd {
	case "self-update":
		if len(rest) > 1 || len(rest) == 1 && rest[0] != "--force" {
			return badUsage()
		}
		if err := selfUpdate(len(rest) == 1); err != nil {
			logf("%v", err)
			return exitError
		}
		return exitOK
	case "setup", "set-username", "set-password", "set-otp-secret":
		if len(rest) != 0 {
			return badUsage()
		}
		set := map[string]func(string) error{"setup": setup, "set-username": setUsername,
			"set-password": setPassword, "set-otp-secret": setOTPSecret}[cmd]
		if err := set(cfgPath); err != nil {
			logf("%v", err)
			return exitError
		}
		return exitOK
	}
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		logf("%v", err)
		return exitError
	}

	switch cmd {
	case "otp":
		if cfg.otpSecret == "" {
			logf("no authenticator secret in %s; run: xivlite set-otp-secret", cfgPath)
			return exitError
		}
		key, err := otpKey(cfg.otpSecret)
		if err != nil {
			logf("%v", err)
			return exitError
		}
		left := 30 - time.Now().Unix()%30
		fmt.Printf("%s (valid for %ds)\n", totpAt(key, time.Now()), left)
		return exitOK

	case "check":
		exe := ""
		if len(rest) == 2 && rest[0] == "--exe" {
			exe = rest[1]
		} else if len(rest) != 0 {
			return badUsage()
		}
		g, err := resolveInstall(cfg, exe)
		if err != nil {
			logf("%v", err)
			return exitError
		}
		if _, err := g.versionReport(0); err != nil {
			logf("%v", err)
			return exitError
		}
		if cfg.otpSecret != "" {
			if _, err := otpKey(cfg.otpSecret); err != nil {
				logf("%v", err)
				return exitError
			}
		}
		fmt.Printf("config ok: %s\ninstall:   %s (game %s, boot %s)\nexe:       %s\n", cfg.path, g.root, g.ver("game"), g.ver("boot"), g.gameExe())
		return exitOK

	case "install", "update":
		root := cfg.gamePath
		if cmd == "install" && len(rest) == 1 {
			root = expandHome(rest[0])
		} else if len(rest) != 0 {
			return badUsage()
		}
		if root == "" {
			logf("no game folder: give one (xivlite install DIR) or run xivlite setup")
			return exitError
		}
		g, err := prepareFolder(cfg, root, cmd == "install")
		if err != nil {
			logf("%v", err)
			return exitError
		}
		if _, err := syncGame(cfg, g, false); err != nil {
			logf("%v", err)
			return exitError
		}
		return exitOK

	case "login":
		exe, out := "", ""
		for len(rest) > 0 {
			if len(rest) < 2 || (rest[0] != "--exe" && rest[0] != "--out") {
				return badUsage()
			}
			if rest[0] == "--exe" {
				exe = rest[1]
			} else {
				out = rest[1]
			}
			rest = rest[2:]
		}
		p, err := prepareLaunch(cfg, exe)
		if err != nil {
			logf("%v", err)
			return exitError
		}
		w := io.Writer(os.Stdout)
		if out != "" {
			f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				logf("%v", err)
				return exitError
			}
			defer f.Close()
			w = f
		}
		if err := writePlan(w, p); err != nil {
			logf("%v", err)
			return exitError
		}
		if p.patch {
			return exitPatch
		}
		return exitOK

	case "exec":
		if len(rest) > 0 && rest[0] == "--" {
			rest = rest[1:]
		}
		if len(rest) == 0 {
			return badUsage()
		}
		p, err := prepareLaunch(cfg, exeIn(rest))
		if err != nil {
			logf("%v", err)
			return exitError
		}
		final := spliceCommand(rest, p)
		path, err := exec.LookPath(final[0])
		if err != nil {
			logf("%v", err)
			return exitError
		}
		_ = os.Chdir(filepath.Dir(p.exe))
		logf("starting %s via %s", filepath.Base(p.exe), final[0])
		err = syscall.Exec(path, final, os.Environ())
		logf("exec %s: %v", path, err)
		return exitError
	}
	return badUsage()
}

func badUsage() int {
	fmt.Fprintf(os.Stderr, usage, version)
	return exitUsage
}

func main() { os.Exit(run(os.Args[1:])) }
