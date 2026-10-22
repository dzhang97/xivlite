# xivlite

A stripped-down [XIVLauncher.Core](https://github.com/goatcorp/XIVLauncher.Core): it installs and
patches FINAL FANTASY XIV, logs a Square Enix (non-Steam) account in and starts the game, nothing
else. It is one static x86_64
Linux binary (no libc, no shared libraries, no .NET, no UI toolkit) plus a short bash script, built
to run under [DroidDeck](https://github.com/Droid-Deck/DroidDeck), where x86 programs go through
FEX and the game itself through DroidDeck's ARM64 Proton.

## What it keeps and drops

Kept, ported from XIVLauncher.Common: the login gate check, the OAuth login (including one-time
passwords), session registration with the version report (boot exe hashes, expansion versions), the
launch arguments, encrypted exactly as the official launcher does (checked byte-for-byte against
XIVLauncher's own `ArgumentBuilder`), and the patcher: fresh installs and updates download Square
Enix's patches, verify them (SHA-1 per block, or chunk CRCs for boot patches), and apply them with a
ZiPatch implementation checked file-for-file against XIVLauncher's own.

Dropped: the UI, Steam accounts, Dalamud and plugins, Wine/DXVK downloads and prefix management
(Proton or your Wine does that), game file repair, news, and the unique-id cache.

## How it fits together

```
Steam / DroidDeck ── xivlite-wrap ──(FEX on ARM)── xivlite login   → writes the launch plan
                          │
                          └── exec <Proton command> game/ffxiv_dx11.exe <session arguments>
```

`xivlite-wrap` (bash) stays native, so the Proton it hands the game to is started exactly as Steam
would start it. Only the login helper is x86_64: on an ARM device the script runs it through
`droiddeck-fex run`; on an x86_64 machine it runs it directly.

## DroidDeck setup

1. From the DroidDeck desktop's terminal, unpack the release (`xivlite-linux-x86_64.tar.gz`) in
   your home folder and run `xivlite/install.sh`. It installs to `~/.local/share/xivlite` (programs
   cannot be run from Android shared storage, which is noexec) and starts `xivlite setup`.
2. Setup asks for your Square Enix ID, password, authenticator secret (if the account uses the
   Software Authenticator) and the game folder, and writes the config with the account details
   encrypted. For a folder without the game, such as `/root/Storage/FFXIV`, it offers to download
   and install it there: about 100 GB, so pick storage with room. The install shows its progress
   and can be interrupted; `xivlite install` resumes it, keeping finished downloads. No file needs
   editing; run `xivlite setup` again to change anything (Enter keeps a saved value).
3. In DroidDeck's Games tab, press **+** and add `game/ffxiv_dx11.exe`. In its editor (✎), set
   **Launch options** to:

   ```
   /root/.local/share/xivlite/xivlite-wrap %command%
   ```

   The script finds the install from the exe in Steam's command. Adding `boot/ffxivboot.exe`
   instead also works; it is swapped for the game exe.

Without `%command%` (from the terminal, say) `xivlite-wrap` runs the game with
`droiddeck-proton-run`, which uses the same Proton and prefix as the Steam shortcut for that exe.
The helper runs under DroidDeck's FEX. If FEX is not installed yet, the first launch asks Steam to
install it (FEX and Steam Linux Runtime 3.0) and stops; launch again once that download finishes.

## Keeping things up to date

- **Game patches** are installed when you press Play, before the game starts. A launch from Steam
  shows no progress, so for a big patch run `xivlite update` in the terminal first and watch it.
- **xivlite itself**: `xivlite self-update` downloads the newest release from GitHub, checks it
  against the release's SHA256SUMS and replaces `xivlite` and `xivlite-wrap`. The config is kept.

Untested on hardware: this was built against DroidDeck's source, and the login and patch protocols
were tested against a stand-in server, not Square Enix.

## Other Linux machines

On x86_64 the binary also works on its own, with no script:

```
xivlite exec -- wine                       # appends game/ffxiv_dx11.exe and its arguments (needs game_path)
xivlite-wrap %command%                     # Steam launch options for a non-Steam shortcut to the game exe
```

## Configuration

`xivlite setup` writes `~/.config/xivlite/config` (or `$XIVLITE_CONFIG`, or `-c FILE`); the other
settings are optional and listed in `config.example`.

| Key | Default | |
| --- | --- | --- |
| `username_encrypted` | | written by setup; or `username` in plain text |
| `password_encrypted` | | written by setup |
| `password_command` | | e.g. `secret-tool lookup ...`; first line of output |
| `password`, `password_file` | | plain text, accepted with a warning |
| `otp_secret_encrypted` | none | written by setup; else `otp_secret` (plain), `XIVLITE_OTP` or a prompt |
| `game_path` | from the command | folder holding `boot/` and `game/` |
| `language` | english | japanese, english, german, french |
| `encrypt_args` | yes | pass arguments encrypted like the official launcher |
| `on_patch` | install | at launch: `install` patches first; `boot` starts the official launcher; `fail` stops |
| `patch_dir` | `.xivlite-patches` in the game folder | where patches wait to be installed |
| `check_gate` | yes | stop early during maintenance |
| `free_trial` | no | free trial accounts |
| `extra_args` | none | extra `key=value` game arguments (no spaces) |

Exactly one password setting may be present.

### How the password is protected

Setup encrypts the ID, password and authenticator secret with AES-256-GCM under a random key in
`~/.local/share/xivlite/secret.key` (mode 600), kept apart from the config. The config can then be
shared, synced or pasted into a bug report without giving the account away, and deleting the key
makes the saved values useless. The log shows the ID masked (`wa*****`).

It is not a lock against software running as your user: anything that can read both files can
decrypt them, as xivlite itself does to log in unattended. DroidDeck's Linux session has no secure
key store that could do better (Android's Keystore is not reachable from it). On a desktop with a
keyring, `password_command` with `secret-tool` keeps the password there instead.

xivlite warns when the config is readable by others. Passwords, codes and session ids never reach
the log (`~/.local/state/xivlite/xivlite.log`).

## Commands and exit status

`xivlite setup`, `install [DIR]`, `update`, `self-update [--force]`, `login [--exe EXE] [--out FILE]`,
`exec [--] CMD...`, `check [--exe EXE]`, `otp`, `version`, and `set-username`, `set-password`,
`set-otp-secret` to change one value. Exit status 0 means start the game, 3 means the plan starts
the official launcher instead (`on_patch = boot`), 1 an error, 2 usage. A failed login shows a desktop notification when one can be
shown, and its reason is the log's last line.

The patcher needs free space for the installed result plus a few patches waiting to install; it
says how much before starting. `XIVLITE_IGNORE_SPACE=1` skips that check.

## Building and testing

```
./build.sh                                   # Go 1.22+, stdlib only → dist/ and the release assets
go test ./...                                # Blowfish, argument encryption vs XIVLauncher, TOTP, secrets, self-update
go test -tags xivlite_mock ./...             # ZiPatch vs XIVLauncher; login, fresh install and updates against a stand-in server
```

`testdata/argvectors.txt` holds output from XIVLauncher's C# `ArgumentBuilder` for the same
arguments, and `testdata/zipatch_digest.txt` the file tree XIVLauncher's C# ZiPatch code produces
from the test patches. One deliberate match: XIVLauncher's "remove all" patch operation deletes
nothing (it builds an invalid path for each file), and xivlite keeps that proven behavior. The Go code has no architecture-specific parts besides the build flags.

Licence: GPL-3.0, as XIVLauncher.
