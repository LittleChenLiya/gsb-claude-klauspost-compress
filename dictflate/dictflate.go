// Package dictflate wraps the existing RFC 1951 DEFLATE stream format of the
// flate package with a small fixed header that records the preset dictionary
// used to produce the stream.
//
// The wrapper does not change the DEFLATE entry points or the byte layout of
// the DEFLATE stream itself: the payload is a plain, single-member DEFLATE
// stream produced by flate.NewWriterDict. The header carries:
//
//   - a magic/version tag,
//   - the FNV-1a digest of the preset dictionary, so a decoder can verify the
//     dictionary before a single byte of DEFLATE data is inflated,
//   - the original (uncompressed) length,
//   - an IEEE CRC-32 (Castagnoli) checksum of the uncompressed data.
//
// Dictionaries are never embedded in the output and must be supplied by the
// calling process. Exactly one DEFLATE data segment is allowed: trailing
// bytes, truncated streams, length and checksum mismatches are all rejected
// and never delivered as a successful decode.
package dictflate

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"hash/fnv"
	"io"
	"math"

	"github.com/klauspost/compress/flate"
)

const (
	// magic0, magic1, version are the three leading header bytes ("DF\x01").
	magic0  byte = 'D'
	magic1  byte = 'F'
	version byte = 1

	// headerSize is the fixed on-disk size of the wrapper header:
	//
	//	magic0, magic1, version   3 bytes
	//	reserved (must be zero)   1 byte
	//	dict digest FNV-1a 64     8 bytes, big endian
	//	original length           8 bytes, big endian
	//	data CRC-32C              4 bytes, big endian
	headerSize = 3 + 1 + 8 + 8 + 4

	reserveSlot = 3
	digestOff   = 4
	lengthOff   = 12
	crcOff      = 20
)

// Errors returned for malformed or mismatching input. They are returned
// directly (not wrapped) so callers can use errors.Is with them.
var (
	// ErrShortHeader means fewer than HeaderSize bytes were supplied.
	ErrShortHeader = errors.New("dictflate: short header")
	// ErrMagic means the leading magic/version bytes do not match.
	ErrMagic = errors.New("dictflate: bad magic or unsupported version")
	// ErrReserved means a reserved header byte was non-zero.
	ErrReserved = errors.New("dictflate: non-zero reserved header field")
	// ErrDictMismatch means the supplied dictionary digest does not match the
	// digest recorded in the header. DEFLATE decoding is never attempted.
	ErrDictMismatch = errors.New("dictflate: dictionary mismatch")
	// ErrTrailingData means bytes follow the single DEFLATE segment.
	ErrTrailingData = errors.New("dictflate: trailing data after deflate segment")
	// ErrLength means the inflated length does not match the header.
	ErrLength = errors.New("dictflate: length mismatch")
	// ErrChecksum means the inflated data checksum does not match the header.
	ErrChecksum = errors.New("dictflate: checksum mismatch")
)

// maxDeclaredLength bounds what a single decoder call is willing to produce
// for inflated output. It stays well below MaxInt so growth arithmetic can
// never overflow.
const maxDeclaredLength = math.MaxInt32

// crcTable is the CRC-32 Castagnoli table reused for every checksum.
var crcTable = crc32.MakeTable(crc32.Castagnoli)

// DictDigest returns the FNV-1a 64-bit digest stored in the header for dict.
// An empty dictionary and a nil dictionary have the same digest.
func DictDigest(dict []byte) uint64 {
	h := fnv.New64a()
	h.Write(dict)
	return h.Sum64()
}

// Encode compresses src with the given preset dictionary and appends the
// wrapper header and the DEFLATE stream to dst, returning the new slice.
//
// level accepts the same constants as the flate package
// (flate.NoCompression .. flate.BestCompression, or flate.DefaultCompression).
// dst may be nil. dict may be nil; the digest recorded then matches a
// nil/empty dictionary on decode.
//
// Exactly one DEFLATE member is emitted; the original DEFLATE writer entry
// point (flate.NewWriterDict) is used unchanged.
func Encode(dst, src, dict []byte, level int) ([]byte, error) {
	// Validate level through the original entry point before reserving space.
	discard, err := flate.NewWriterDict(io.Discard, level, dict)
	if err != nil {
		return nil, err
	}
	discard.Close()

	off := len(dst)
	out := bytes.NewBuffer(dst)
	if _, err := out.Write(make([]byte, headerSize)); err != nil {
		return nil, err
	}

	fw, err := flate.NewWriterDict(out, level, dict)
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(src); err != nil {
		return nil, err
	}
	if err := fw.Close(); err != nil {
		return nil, err
	}

	b := out.Bytes()[off:]
	b[0] = magic0
	b[1] = magic1
	b[2] = version
	b[reserveSlot] = 0
	binary.BigEndian.PutUint64(b[digestOff:], DictDigest(dict))
	binary.BigEndian.PutUint64(b[lengthOff:], uint64(len(src)))
	binary.BigEndian.PutUint32(b[crcOff:], crc32.Checksum(src, crcTable))
	return out.Bytes(), nil
}

// limitedBuffer is an append-only byte buffer that never holds more than
// limit bytes. It grows through append's normal (at most doubling) policy,
// starting empty, so a huge declared length never causes a huge allocation
// before the actual inflated size is known.
type limitedBuffer struct {
	buf      []byte
	limit    int
	overshot bool
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if len(p) > l.limit-len(l.buf) {
		l.overshot = true
		return 0, ErrLength
	}
	l.buf = append(l.buf, p...)
	return len(p), nil
}

// Decode verifies the wrapper header against dict and, only when the
// dictionary matches, inflates the single DEFLATE segment and checks its
// length and checksum. It returns the uncompressed bytes and nil only when
// every check passes; any corruption or mismatch returns an error and no
// data is delivered.
//
// dst may be nil and is used as initial storage for the result, like append.
// dict is provided in-process by the caller and is never read from the
// stream. A nil dict matches only data encoded with a nil/empty dictionary.
func Decode(dst, src, dict []byte) ([]byte, error) {
	// --- Strict header checks; none of these allocate for the payload. ---
	if len(src) < headerSize {
		return nil, ErrShortHeader
	}
	hdr := src[:headerSize]
	if hdr[0] != magic0 || hdr[1] != magic1 || hdr[2] != version {
		return nil, ErrMagic
	}
	if hdr[reserveSlot] != 0 {
		return nil, ErrReserved
	}
	wantDigest := binary.BigEndian.Uint64(hdr[digestOff:])
	if got := DictDigest(dict); got != wantDigest {
		// Refuse before touching DEFLATE: a wrong dictionary must never be
		// fed to the inflater and yield plausible-looking output.
		return nil, ErrDictMismatch
	}
	origLen64 := binary.BigEndian.Uint64(hdr[lengthOff:])
	wantCRC := binary.BigEndian.Uint32(hdr[crcOff:])
	if origLen64 > maxDeclaredLength {
		// Huge declared length: never allocate for it, never succeed.
		return nil, ErrLength
	}

	// --- Inflate, capped by the declared original length. ---
	br := bytes.NewReader(src[headerSize:])
	fr := flate.NewReaderDict(br, dict)

	lb := &limitedBuffer{limit: int(origLen64)}
	if _, err := io.Copy(lb, fr); err != nil {
		fr.Close()
		return nil, err
	}
	if err := fr.Close(); err != nil {
		return nil, err
	}

	// Exactly one segment: nothing may follow the DEFLATE stream.
	if br.Len() != 0 {
		return nil, ErrTrailingData
	}
	if lb.overshot || len(lb.buf) != int(origLen64) {
		return nil, ErrLength
	}
	if crc32.Checksum(lb.buf, crcTable) != wantCRC {
		return nil, ErrChecksum
	}
	return append(dst, lb.buf...), nil
}
