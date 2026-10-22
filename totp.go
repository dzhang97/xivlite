package main

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// otpKey accepts a base32 secret (spaces, dashes and padding ignored) or an otpauth:// URI.
func otpKey(secret string) ([]byte, error) {
	secret = strings.TrimSpace(secret)
	if strings.HasPrefix(strings.ToLower(secret), "otpauth://") {
		u, err := url.Parse(secret)
		if err != nil {
			return nil, fmt.Errorf("otp_secret: %v", err)
		}
		secret = u.Query().Get("secret")
	}
	s := strings.ToUpper(strings.NewReplacer(" ", "", "-", "", "=", "").Replace(secret))
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(s)
	if err != nil || len(key) == 0 {
		return nil, fmt.Errorf("otp_secret is not a valid base32 secret")
	}
	return key, nil
}

// totpAt is RFC 6238 with Square Enix's parameters: SHA-1, 30 s steps, 6 digits.
func totpAt(key []byte, t time.Time) string {
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(t.Unix()/30))
	mac := hmac.New(sha1.New, key)
	mac.Write(counter[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	code := (binary.BigEndian.Uint32(sum[off:]) & 0x7fffffff) % 1000000
	return fmt.Sprintf("%06d", code)
}

// freshTOTP waits out the last seconds of a step so the code is not already stale on arrival.
func freshTOTP(key []byte) string {
	now := time.Now()
	if left := 30 - now.Unix()%30; left <= 3 {
		time.Sleep(time.Duration(left)*time.Second + 200*time.Millisecond)
		now = time.Now()
	}
	return totpAt(key, now)
}
