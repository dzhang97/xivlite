package main

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBlowfishStandardVectors(t *testing.T) {
	// Eric Young's published vectors, with keys on which SE's sign quirk changes nothing;
	// halves are big-endian in those, so compare words.
	for _, v := range []struct{ key, pt, ct string }{
		{"0000000000000000", "0000000000000000", "4EF997456198DD78"},
		{"FFFFFFFFFFFFFFFF", "FFFFFFFFFFFFFFFF", "51866FD5B85ECB8A"},
		{"3000000000000000", "1000000000000001", "7D856F9A613063F2"},
		{"1111111111111111", "0123456789ABCDEF", "7D0CC630AFDA1EC7"},
	} {
		key, _ := hex.DecodeString(v.key)
		pt, _ := hex.DecodeString(v.pt)
		ct, _ := hex.DecodeString(v.ct)
		l, r := newLegacyBlowfish(key).block(binary.BigEndian.Uint32(pt), binary.BigEndian.Uint32(pt[4:]))
		if l != binary.BigEndian.Uint32(ct) || r != binary.BigEndian.Uint32(ct[4:]) {
			t.Errorf("key %s: got %08X%08X want %s", v.key, l, r, v.ct)
		}
	}
}

func TestLegacyBlowfishMatchesXIVLauncher(t *testing.T) {
	// Output of XIVLauncher's LegacyBlowfish with key bytes >= 0x80 (exercises the sign quirk).
	got := strings.ToUpper(hex.EncodeToString(newLegacyBlowfish([]byte{0x80, 0xff, 0x41, 0xc3, 0x7f}).
		encrypt([]byte("hello world, blowfish!"))))
	if want := "6E2DA544BBEEF91A16E91CCDFF98BA197DD5B466944299F2"; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func referenceArgs() []gameArg {
	return []gameArg{
		{"DEV.DataPathType", "1"}, {"DEV.MaxEntitledExpansionID", "5"},
		{"DEV.TestSID", "0123456789abcdef0123456789abcdef0123456789abcdef01234567"},
		{"DEV.UseSqPack", "1"}, {"SYS.Region", "3"}, {"language", "1"},
		{"resetConfig", "0"}, {"ver", "2026.10.01.0000.0000"}, {"UserPath", `C:\a b`},
	}
}

func TestEncryptedArgsMatchXIVLauncher(t *testing.T) {
	// testdata/argvectors.txt: XIVLauncher's ArgumentBuilder.BuildEncrypted on the same arguments.
	f, err := os.Open("testdata/argvectors.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	n := 0
	for sc.Scan() {
		ticksText, want, _ := strings.Cut(sc.Text(), " ")
		ticks, _ := strconv.ParseUint(ticksText, 10, 32)
		got := encryptedArgs(referenceArgs(), uint32(ticks))
		if got != want {
			t.Errorf("ticks %d:\n got %s\nwant %s", ticks, got, want)
		}
		plain, err := decryptArgs(got, uint32(ticks))
		if err != nil || !strings.HasPrefix(plain, " /T ="+ticksText+" /DEV.DataPathType =1") || !strings.HasSuffix(plain, ` /UserPath =C:\a  b`) {
			t.Errorf("decrypt: %q %v", plain, err)
		}
		n++
	}
	if n == 0 {
		t.Fatal("no vectors")
	}
}

func TestTOTPRFC6238(t *testing.T) {
	key, err := otpKey("GEZD GNBV-GY3T QOJQ GEZDGNBVGY3TQOJQ")
	if err != nil {
		t.Fatal(err)
	}
	for unix, want := range map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924", 2000000000: "279037"} {
		if got := totpAt(key, time.Unix(unix, 0)); got != want {
			t.Errorf("t=%d got %s want %s", unix, got, want)
		}
	}
	uriKey, err := otpKey("otpauth://totp/SE:me?secret=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ&issuer=SE")
	if err != nil || string(uriKey) != string(key) {
		t.Errorf("otpauth URI: %v", err)
	}
	if _, err := otpKey("not base32!"); err == nil {
		t.Error("bad secret accepted")
	}
}

func TestSpliceCommand(t *testing.T) {
	p := &plan{exe: "/g/game/ffxiv_dx11.exe", args: []string{"A=1", "B=2"}}
	got := strings.Join(spliceCommand([]string{"reaper", "--", "/p/proton", "waitforexitandrun", "/g/boot/FFXIVBoot.exe", "-x"}, p), " ")
	if want := "reaper -- /p/proton waitforexitandrun /g/game/ffxiv_dx11.exe A=1 B=2 -x"; got != want {
		t.Errorf("got %s", got)
	}
	got = strings.Join(spliceCommand([]string{"wine"}, p), " ")
	if want := "wine /g/game/ffxiv_dx11.exe A=1 B=2"; got != want {
		t.Errorf("got %s", got)
	}
	if installRootFromExe("/g/boot/ffxivboot64.exe") != "/g" || installRootFromExe("/x/ffxiv_dx11.exe") != "" {
		t.Error("installRootFromExe")
	}
}
