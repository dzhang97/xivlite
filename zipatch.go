package main

// ZiPatch: Square Enix's patch format, applied in place to boot/ or game/.
// Port of XIVLauncher.Common/Patching/ZiPatch.

import (
	"bufio"
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var zipatchMagic = []byte("\x91ZIPATCH\r\n\x1a\n")

const maxChunkSize = 512 << 20

type chunkReader struct {
	r   *bufio.Reader
	buf []byte
}

func openPatch(r io.Reader) (*chunkReader, error) {
	cr := &chunkReader{r: bufio.NewReaderSize(r, 1<<20)}
	magic := make([]byte, len(zipatchMagic))
	if _, err := io.ReadFull(cr.r, magic); err != nil || !bytes.Equal(magic, zipatchMagic) {
		return nil, errors.New("not a ZiPatch file")
	}
	return cr, nil
}

// next returns the chunk type and body; verify checks the CRC32 over type and body.
func (c *chunkReader) next(verify bool) (string, []byte, error) {
	var head [4]byte
	if _, err := io.ReadFull(c.r, head[:]); err != nil {
		return "", nil, fmt.Errorf("truncated patch: %v", err)
	}
	size := binary.BigEndian.Uint32(head[:])
	if size > maxChunkSize {
		return "", nil, fmt.Errorf("chunk of %d bytes is too large", size)
	}
	need := int(size) + 8
	if cap(c.buf) < need {
		c.buf = make([]byte, need)
	}
	buf := c.buf[:need]
	if _, err := io.ReadFull(c.r, buf); err != nil {
		return "", nil, fmt.Errorf("truncated patch: %v", err)
	}
	if verify && crc32.ChecksumIEEE(buf[:4+size]) != binary.BigEndian.Uint32(buf[4+size:]) {
		return "", nil, fmt.Errorf("%s chunk fails its checksum", buf[:4])
	}
	return string(buf[:4]), buf[4 : 4+size], nil
}

// cursor reads the fields of a chunk body.
type cursor struct {
	b   []byte
	err error
}

func (c *cursor) take(n int) []byte {
	if c.err != nil || n < 0 || n > len(c.b) {
		c.err = errors.New("chunk is shorter than its fields")
		if n > 0 && n <= 8 {
			return make([]byte, n)
		}
		return nil
	}
	out := c.b[:n]
	c.b = c.b[n:]
	return out
}

func (c *cursor) u8() byte    { return c.take(1)[0] }
func (c *cursor) u16() uint16 { return binary.BigEndian.Uint16(c.take(2)) }
func (c *cursor) u32() uint32 { return binary.BigEndian.Uint32(c.take(4)) }
func (c *cursor) i64() int64  { return int64(binary.BigEndian.Uint64(c.take(8))) }
func (c *cursor) le32() int32 { return int32(binary.LittleEndian.Uint32(c.take(4))) }
func (c *cursor) str(n int) string {
	return strings.TrimRight(string(c.take(n)), "\x00")
}

type applier struct {
	root     string
	platform uint16
	files    map[string]*os.File
}

func newApplier(root string) *applier {
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return &applier{root: filepath.Clean(root), files: map[string]*os.File{}}
}

var platformNames = map[uint16]string{0: "win32", 1: "ps3", 2: "ps4", 3: "unknown"}

func expansionFolder(id byte) string {
	if id == 0 {
		return "ffxiv"
	}
	return fmt.Sprintf("ex%d", id)
}

// target resolves a patch-relative path inside the root, refusing anything that escapes it.
func (a *applier) target(rel string) (string, error) {
	rel = strings.ReplaceAll(rel, `\`, "/")
	p := filepath.Join(a.root, rel)
	if p != a.root && !strings.HasPrefix(p, a.root+string(filepath.Separator)) {
		return "", fmt.Errorf("patch names a path outside the game folder: %q", rel)
	}
	return p, nil
}

func (a *applier) open(rel string) (*os.File, error) {
	p, err := a.target(rel)
	if err != nil {
		return nil, err
	}
	if f := a.files[p]; f != nil {
		return f, nil
	}
	if len(a.files) >= 64 {
		a.closeAll()
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	a.files[p] = f
	return f, nil
}

func (a *applier) forget(p string) {
	if f := a.files[p]; f != nil {
		f.Close()
		delete(a.files, p)
	}
}

func (a *applier) closeAll() error {
	var first error
	for p, f := range a.files {
		if err := f.Close(); err != nil && first == nil {
			first = err
		}
		delete(a.files, p)
	}
	return first
}

var zeros = make([]byte, 1<<16)

func zeroAt(f *os.File, off, n int64) error {
	for n > 0 {
		k := min(n, int64(len(zeros)))
		if _, err := f.WriteAt(zeros[:k], off); err != nil {
			return err
		}
		off += k
		n -= k
	}
	return nil
}

// sqpackPath reads a SqPack file reference (main id, sub id, file id).
func (a *applier) sqpackPath(c *cursor, index bool) string {
	mainID, subID, fileID := c.u16(), c.u16(), c.u32()
	name := fmt.Sprintf("sqpack/%s/%02x%04x.%s", expansionFolder(byte(subID>>8)), mainID, subID, platformNames[a.platform])
	if index {
		if fileID == 0 {
			return name + ".index"
		}
		return fmt.Sprintf("%s.index%d", name, fileID)
	}
	return fmt.Sprintf("%s.dat%d", name, fileID)
}

// emptyBlock marks a deleted or expanded dat region: zeros, then an empty-block header.
func emptyBlock(f *os.File, off, blocks int64) error {
	if err := zeroAt(f, off, blocks<<7); err != nil {
		return err
	}
	var h [24]byte
	binary.LittleEndian.PutUint32(h[0:], 1<<7)
	binary.LittleEndian.PutUint64(h[12:], uint64(blocks-1))
	_, err := f.WriteAt(h[:], off)
	return err
}

func (a *applier) applyPatch(r io.Reader, verifyCRC bool) error {
	cr, err := openPatch(r)
	if err != nil {
		return err
	}
	defer a.closeAll()
	for {
		typ, body, err := cr.next(verifyCRC)
		if err != nil {
			return err
		}
		c := &cursor{b: body}
		switch typ {
		case "EOF_":
			return a.closeAll()
		case "FHDR", "APLY", "APFS", "XXXX":
		case "ADIR", "DELD":
			name := c.str(int(c.u32()))
			if c.err != nil {
				return c.err
			}
			p, err := a.target(name)
			if err != nil {
				return err
			}
			if typ == "ADIR" {
				err = os.MkdirAll(p, 0o755)
			} else {
				err = os.Remove(p)
			}
			if err != nil {
				return err
			}
		case "SQPK":
			if int(c.u32()) != len(body) {
				return errors.New("SQPK chunk size mismatch")
			}
			cmd := c.u8()
			if err := a.sqpk(cmd, c); err != nil {
				return fmt.Errorf("SQPK %c: %v", cmd, err)
			}
		default:
			return fmt.Errorf("unknown chunk type %q", typ)
		}
	}
}

func (a *applier) sqpk(cmd byte, c *cursor) error {
	switch cmd {
	case 'T':
		c.take(3)
		a.platform = c.u16()
		return c.err
	case 'X', 'I':
		return nil
	case 'A':
		c.take(3)
		path := a.sqpackPath(c, false)
		off, n, del := int64(c.u32())<<7, int64(c.u32())<<7, int64(c.u32())<<7
		data := c.take(int(n))
		if c.err != nil {
			return c.err
		}
		f, err := a.open(path)
		if err != nil {
			return err
		}
		if _, err := f.WriteAt(data, off); err != nil {
			return err
		}
		return zeroAt(f, off+n, del)
	case 'D', 'E':
		c.take(3)
		path := a.sqpackPath(c, false)
		off, blocks := int64(c.u32())<<7, int64(c.u32())
		if c.err != nil {
			return c.err
		}
		f, err := a.open(path)
		if err != nil {
			return err
		}
		return emptyBlock(f, off, blocks)
	case 'H':
		fileKind, headerKind := c.u8(), c.u8()
		c.take(1)
		path := a.sqpackPath(c, fileKind == 'I')
		data := c.take(1024)
		if c.err != nil {
			return c.err
		}
		f, err := a.open(path)
		if err != nil {
			return err
		}
		off := int64(1024)
		if headerKind == 'V' {
			off = 0
		}
		_, err = f.WriteAt(data, off)
		return err
	case 'F':
		return a.sqpkFile(c)
	}
	return fmt.Errorf("unknown command")
}

func (a *applier) sqpkFile(c *cursor) error {
	op := c.u8()
	c.take(2)
	fileOffset := c.i64()
	c.i64() // file size
	pathLen := c.u32()
	expansion := c.u16()
	c.take(2)
	rel := c.str(int(pathLen))
	if c.err != nil {
		return c.err
	}
	switch op {
	case 'R':
		return a.removeAll(byte(expansion))
	case 'D':
		p, err := a.target(rel)
		if err != nil {
			return err
		}
		a.forget(p)
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	case 'M':
		p, err := a.target(rel)
		if err != nil {
			return err
		}
		return os.MkdirAll(p, 0o755)
	}
	f, err := a.open(rel)
	if err != nil {
		return err
	}
	if fileOffset == 0 {
		if err := f.Truncate(0); err != nil {
			return err
		}
	}
	off := fileOffset
	for len(c.b) > 0 {
		headerSize, _, compressed, decompressed := c.le32(), c.le32(), c.le32(), c.le32()
		isCompressed := compressed != 0x7d00
		size := decompressed
		if isCompressed {
			size = compressed
		}
		blockLen := (int(size) + 143) &^ 127
		payload := c.take(blockLen - int(headerSize))
		if c.err != nil {
			return c.err
		}
		var data []byte
		if isCompressed {
			if data, err = io.ReadAll(flate.NewReader(bytes.NewReader(payload))); err != nil {
				return fmt.Errorf("%s: %v", rel, err)
			}
		} else {
			if decompressed < 0 || int(decompressed) > len(payload) {
				return fmt.Errorf("%s: malformed stored block", rel)
			}
			data = payload[:decompressed]
		}
		if _, err := f.WriteAt(data, off); err != nil {
			return err
		}
		off += int64(len(data))
	}
	return nil
}

// removeAll is the SqpkFile RemoveAll operation, which is meant to clear an expansion's sqpack and
// movie folders before the patch adds them again. XIVLauncher's version deletes nothing (it joins
// each listed absolute path onto the game folder again), and installs have always relied on that,
// so xivlite matches it: the patch overwrites every file it ships, and stale extras are harmless.
func (a *applier) removeAll(expansion byte) error {
	return nil
}

// checkPatchCRC reads a whole patch, checking every chunk's CRC (boot patches carry no hashes).
func checkPatchCRC(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	cr, err := openPatch(f)
	if err != nil {
		return err
	}
	for {
		typ, _, err := cr.next(true)
		if err != nil {
			return err
		}
		if typ == "EOF_" {
			return nil
		}
	}
}

func applyPatchFile(path, root string, verifyCRC bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	return newApplier(root).applyPatch(f, verifyCRC)
}
