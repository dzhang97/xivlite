//go:build xivlite_mock

// Builds ZiPatch files for tests and the mock server.

package main

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"hash/crc32"
)

type zpWriter struct{ buf bytes.Buffer }

func newZiPatch() *zpWriter {
	w := &zpWriter{}
	w.buf.Write(zipatchMagic)
	fhdr := make([]byte, 0x100)
	fhdr[2] = 3
	copy(fhdr[4:], "DIFF")
	w.chunk("FHDR", fhdr)
	w.chunk("APLY", be32s(1, 4, 0))
	w.chunk("APLY", be32s(2, 4, 0))
	return w
}

func be32s(vs ...uint32) []byte {
	out := make([]byte, 4*len(vs))
	for i, v := range vs {
		binary.BigEndian.PutUint32(out[4*i:], v)
	}
	return out
}

func (w *zpWriter) chunk(typ string, body []byte) {
	var head [4]byte
	binary.BigEndian.PutUint32(head[:], uint32(len(body)))
	w.buf.Write(head[:])
	crcInput := append([]byte(typ), body...)
	w.buf.Write(crcInput)
	var sum [4]byte
	binary.BigEndian.PutUint32(sum[:], crc32.ChecksumIEEE(crcInput))
	w.buf.Write(sum[:])
}

func (w *zpWriter) sqpk(cmd byte, body []byte) {
	full := make([]byte, 5, 5+len(body))
	binary.BigEndian.PutUint32(full, uint32(5+len(body)))
	full[4] = cmd
	w.chunk("SQPK", append(full, body...))
}

func sqpackRef(mainID, subID uint16, fileID uint32) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint16(b, mainID)
	binary.BigEndian.PutUint16(b[2:], subID)
	binary.BigEndian.PutUint32(b[4:], fileID)
	return b
}

func (w *zpWriter) targetInfo(platform uint16) {
	b := make([]byte, 3+2+2+2+2+8+8+96)
	binary.BigEndian.PutUint16(b[3:], platform)
	binary.BigEndian.PutUint16(b[5:], 0xffff)
	w.sqpk('T', b)
}

// addData writes data (a multiple of 128 bytes) at block offset off, then zeroes del blocks.
func (w *zpWriter) addData(mainID, subID uint16, fileID, off uint32, data []byte, del uint32) {
	b := append([]byte{0, 0, 0}, sqpackRef(mainID, subID, fileID)...)
	b = append(b, be32s(off, uint32(len(data)>>7), del)...)
	w.sqpk('A', append(b, data...))
}

func (w *zpWriter) emptyData(cmd byte, mainID, subID uint16, fileID, off, blocks uint32) {
	b := append([]byte{0, 0, 0}, sqpackRef(mainID, subID, fileID)...)
	w.sqpk(cmd, append(b, be32s(off, blocks, 0)...))
}

func (w *zpWriter) header(fileKind, headerKind byte, mainID, subID uint16, fileID uint32, data []byte) {
	b := append([]byte{fileKind, headerKind, 0}, sqpackRef(mainID, subID, fileID)...)
	h := make([]byte, 1024)
	copy(h, data)
	w.sqpk('H', append(b, h...))
}

func (w *zpWriter) fileOp(op byte, path string, offset int64, expansion uint16, blocks []byte) {
	b := []byte{op, 0, 0}
	b = binary.BigEndian.AppendUint64(b, uint64(offset))
	b = binary.BigEndian.AppendUint64(b, 0)
	name := append([]byte(path), 0)
	b = binary.BigEndian.AppendUint32(b, uint32(len(name)))
	b = binary.BigEndian.AppendUint16(b, expansion)
	b = append(append(b, 0, 0), name...)
	w.sqpk('F', append(b, blocks...))
}

// addFile writes data at offset as blocks of up to 16000 bytes, alternately deflated and stored.
func (w *zpWriter) addFile(path string, offset int64, data []byte) {
	var blocks []byte
	for i := 0; len(data) > 0 || i == 0; i++ {
		n := min(len(data), 16000)
		part := data[:n]
		data = data[n:]
		var payload []byte
		compressed := uint32(0x7d00)
		if i%2 == 0 {
			var z bytes.Buffer
			fw, _ := flate.NewWriter(&z, flate.BestCompression)
			fw.Write(part)
			fw.Close()
			payload = z.Bytes()
			compressed = uint32(len(payload))
		} else {
			payload = part
		}
		blockLen := (len(payload) + 143) &^ 127
		head := make([]byte, 16)
		binary.LittleEndian.PutUint32(head, 16)
		binary.LittleEndian.PutUint32(head[8:], compressed)
		binary.LittleEndian.PutUint32(head[12:], uint32(len(part)))
		blocks = append(blocks, head...)
		blocks = append(blocks, payload...)
		blocks = append(blocks, make([]byte, blockLen-16-len(payload))...)
		if len(data) == 0 {
			break
		}
	}
	w.fileOp('A', path, offset, 0, blocks)
}

func (w *zpWriter) dir(typ, name string) {
	w.chunk(typ, append(be32s(uint32(len(name))), name...))
}

func (w *zpWriter) bytes() []byte {
	w.chunk("EOF_", nil)
	return w.buf.Bytes()
}
