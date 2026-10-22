# xivlite

A stripped-down [XIVLauncher.Core](https://github.com/goatcorp/XIVLauncher.Core): it logs a Square
Enix (non-Steam) account in and starts FINAL FANTASY XIV, nothing else. It is one static x86_64
Linux binary (no libc, no shared libraries, no .NET, no UI toolkit) plus a short bash script, built
to run under [DroidDeck](https://github.com/Droid-Deck/DroidDeck), where x86 programs go through
FEX and the game itself through DroidDeck's ARM64 Proton.

## What it keeps and drops

Kept, ported from XIVLauncher.Common: the login gate check, the boot version check, the OAuth login
(including one-time passwords), session registration with the version report (boot exe hashes,
expansion versions), and the launch arguments, encrypted exactly as the official launcher does
(checked byte-for-byte against XIVLauncher's own `ArgumentBuilder`).

Dropped: the UI, Steam accounts, Dalamud and plugins, Wine/DXVK downloads and prefix management
(Proton or your Wine does that), the patcher, game repair, news, and the unique-id cache. When the
game needs a patch, xivlite starts the official launcher (`boot/ffxivboot64.exe`) through the same
command instead, so Square Enix's updater does it; log in there once, then go back to xivlite.

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

1. Install the game files from a PC or the official installer, e.g. under internal storage
   (`/root/Storage/...` inside the session).
2. From the DroidDeck desktop's terminal, copy `dist/` somewhere and run `./install.sh`. It installs
   to `~/.local/share/xivlite` (programs cannot be run from Android shared storage, which is noexec)
   and creates `~/.config/xivlite/config`.
3. Edit the config: `username`, `password`, and `otp_secret` if the account has the Software
   Authenticator. Check it with `~/.local/share/xivlite/xivlite check --exe "<install>/game/ffxiv_dx11.exe"`.
4. In DroidDeck's Games tab, press **+** and add `game/ffxiv_dx11.exe`. In its editor (✎), set
   **Launch options** to:

   ```
   /root/.local/share/xivlite/xivlite-wrap %command%
   ```

   The script finds the install from the exe in Steam's command, so `game_path` can stay unset.
   Adding `boot/ffxivboot.exe` instead also works; it is swapped for the game exe.

Without `%command%` (from the terminal, say) `xivlite-wrap` runs the game with
`droiddeck-proton-run`, which uses the same Proton and prefix as the Steam shortcut for that exe.
The helper runs under DroidDeck's FEX. If FEX is not installed yet, the first launch asks Steam to
install it (FEX and Steam Linux Runtime 3.0) and stops; launch again once that download finishes.

Untested on hardware: this was built against DroidDeck's source, and the login protocol was tested
against a stand-in server, not Square Enix.

## Other Linux machines

On x86_64 the binary also works on its own, with no script:

```
xivlite exec -- wine                       # appends game/ffxiv_dx11.exe and its arguments (needs game_path)
xivlite-wrap %command%                     # Steam launch options for a non-Steam shortcut to the game exe
```

## Configuration

`~/.config/xivlite/config` (or `$XIVLITE_CONFIG`, or `-c FILE`); see `config.example`.

| Key | Default | |
| --- | --- | --- |
| `username`, `password` / `password_file` | required | Square Enix ID and password |
| `otp_secret` | none | base32 secret or `otpauth://` URI; otherwise `XIVLITE_OTP` or a terminal prompt |
| `game_path` | from the command | folder holding `boot/` and `game/` |
| `language` | english | japanese, english, german, french |
| `encrypt_args` | yes | pass arguments encrypted like the official launcher |
| `on_patch` | boot | `boot` starts the official launcher to patch; `fail` stops |
| `check_gate` | yes | stop early during maintenance |
| `free_trial` | no | free trial accounts |
| `extra_args` | none | extra `key=value` game arguments (no spaces) |

Keep the file private (`chmod 600`); xivlite warns when it is not. Passwords, codes and session ids
never reach the log (`~/.local/state/xivlite/xivlite.log`).

## Commands and exit status

`xivlite login [--exe EXE] [--out FILE]`, `exec [--] CMD...`, `check [--exe EXE]`, `otp`, `version`.
Exit status 0 means start the game, 3 means patch (the plan names the official launcher), 1 an
error, 2 usage. A failed login shows a desktop notification when one can be shown, and its reason
is the log's last line.

## Building and testing

```
./build.sh                                   # Go 1.22+, stdlib only → dist/
go test ./...                                # Blowfish, argument encryption vs XIVLauncher, TOTP
go test -tags xivlite_mock ./...             # full login flow against a stand-in Square Enix server
```

`testdata/argvectors.txt` holds output from XIVLauncher's C# `ArgumentBuilder` for the same
arguments. The Go code has no architecture-specific parts besides the build flags.

Licence: GPL-3.0, as XIVLauncher.
