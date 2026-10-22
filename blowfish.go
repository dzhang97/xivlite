package main

// legacyBlowfish is Square Enix's Blowfish variant used for the game's launch arguments:
// little-endian block halves, and a key schedule that sign-extends each key byte
// (port of XIVLauncher.Common/Encryption/LegacyBlowfish.cs).
type legacyBlowfish struct {
	p [18]uint32
	s [4][256]uint32
}

func newLegacyBlowfish(key []byte) *legacyBlowfish {
	b := &legacyBlowfish{p: blowfishP, s: blowfishS}
	k := 0
	for i := range b.p {
		var n uint32
		for j := 0; j < 4; j++ {
			n = n<<8 | uint32(int32(int8(key[k%len(key)])))
			k++
		}
		b.p[i] ^= n
	}
	var l, r uint32
	for i := 0; i < len(b.p); i += 2 {
		l, r = b.block(l, r)
		b.p[i], b.p[i+1] = l, r
	}
	for i := range b.s {
		for j := 0; j < len(b.s[i]); j += 2 {
			l, r = b.block(l, r)
			b.s[i][j], b.s[i][j+1] = l, r
		}
	}
	return b
}

func (b *legacyBlowfish) f(x uint32) uint32 {
	return ((b.s[0][x>>24] + b.s[1][(x>>16)&0xff]) ^ b.s[2][(x>>8)&0xff]) + b.s[3][x&0xff]
}

func (b *legacyBlowfish) block(l, r uint32) (uint32, uint32) {
	for i := 0; i < 16; i += 2 {
		l ^= b.p[i]
		r ^= b.f(l)
		r ^= b.p[i+1]
		l ^= b.f(r)
	}
	return r ^ b.p[17], l ^ b.p[16]
}

func (b *legacyBlowfish) unblock(l, r uint32) (uint32, uint32) {
	for i := 16; i > 0; i -= 2 {
		l ^= b.p[i+1]
		r ^= b.f(l)
		r ^= b.p[i]
		l ^= b.f(r)
	}
	return r ^ b.p[0], l ^ b.p[1]
}

func le32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

func putLE32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}

// encrypt zero-pads data to whole 8-byte blocks and encrypts it in ECB mode.
func (b *legacyBlowfish) encrypt(data []byte) []byte {
	buf := make([]byte, (len(data)+7)/8*8)
	copy(buf, data)
	for i := 0; i < len(buf); i += 8 {
		l, r := b.block(le32(buf[i:]), le32(buf[i+4:]))
		putLE32(buf[i:], l)
		putLE32(buf[i+4:], r)
	}
	return buf
}

func (b *legacyBlowfish) decrypt(data []byte) []byte {
	buf := append([]byte(nil), data...)
	for i := 0; i+8 <= len(buf); i += 8 {
		l, r := b.unblock(le32(buf[i:]), le32(buf[i+4:]))
		putLE32(buf[i:], l)
		putLE32(buf[i+4:], r)
	}
	return buf
}
