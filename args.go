package main

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

type gameArg struct{ Key, Value string }

var checksumTable = []byte("fX1pGtdS5CAP4_VL")

// plainArgs returns one "key=value" word per argument.
func plainArgs(args []gameArg) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = a.Key + "=" + a.Value
	}
	return out
}

// encryptedArgs builds the single "//**sqex0003...**//" argument the official launcher passes.
// ticks is the 32-bit millisecond tick count the game compares against its own GetTickCount.
func encryptedArgs(args []gameArg, ticks uint32) string {
	key := ticks & 0xffff0000
	all := append([]gameArg{{"T", strconv.FormatUint(uint64(ticks), 10)}}, args...)
	var sb strings.Builder
	for _, a := range all {
		// SE escapes spaces by doubling them; every pair has a leading space, including the first.
		sb.WriteString(" /" + strings.ReplaceAll(a.Key, " ", "  ") + " =" + strings.ReplaceAll(a.Value, " ", "  "))
	}
	cipher := newLegacyBlowfish([]byte(fmt.Sprintf("%08x", key)))
	b64 := base64.StdEncoding.EncodeToString(cipher.encrypt([]byte(sb.String())))
	b64 = strings.NewReplacer("+", "-", "/", "_", "=", "*").Replace(b64)
	return fmt.Sprintf("//**sqex%04d%s%c**//", 3, b64, checksumTable[(key&0x000f0000)>>16])
}

// decryptArgs reverses encryptedArgs given the key; used by tests and `xivlite decode`.
func decryptArgs(arg string, key uint32) (string, error) {
	if !strings.HasPrefix(arg, "//**sqex0003") || !strings.HasSuffix(arg, "**//") || len(arg) < 17 {
		return "", fmt.Errorf("not an encrypted argument string")
	}
	body := arg[len("//**sqex0003") : len(arg)-len("**//")-1]
	body = strings.NewReplacer("-", "+", "_", "/", "*", "=").Replace(body)
	raw, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return "", err
	}
	plain := newLegacyBlowfish([]byte(fmt.Sprintf("%08x", key&0xffff0000))).decrypt(raw)
	return strings.TrimRight(string(plain), "\x00"), nil
}
