package dictflate

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"runtime"
	"testing"

	"github.com/klauspost/compress/flate"
)

// corpus is repeated to build data that actually benefits from the
// dictionary; it is also used as the dictionary itself.
var corpus = []byte("the quick brown fox jumps over the lazy dog. " +
	"the quick brown fox jumps over the lazy dog. ")

func payload(n int) []byte {
	p := make([]byte, 0, len(corpus)*n)
	for i := 0; i < n; i++ {
		p = append(p, corpus...)
	}
	return p
}

func TestRoundTrip(t *testing.T) {
	dicts := map[string][]byte{
		"nil":           nil,
		"empty":         {},
		"matching-dict": corpus,
	}
	sizes := []int{0, 1, 5, len(corpus), len(corpus) * 12}
	levels := []int{flate.NoCompression, flate.DefaultCompression,
		flate.BestSpeed, flate.BestCompression}

	for dn, dict := range dicts {
		for _, n := range sizes {
			for _, level := range levels {
				src := payload(n)
				enc, err := Encode(nil, src, dict, level)
				if err != nil {
					t.Fatalf("encode dict=%s n=%d level=%d: %v", dn, n, level, err)
				}
				if len(enc) < headerSize {
					t.Fatalf("encoded output shorter than header")
				}
				got, err := Decode(nil, enc, dict)
				if err != nil {
					t.Fatalf("decode dict=%s n=%d level=%d: %v", dn, n, level, err)
				}
				if !bytes.Equal(got, src) {
					t.Fatalf("round-trip mismatch dict=%s n=%d level=%d: "+
						"want %d bytes, got %d", dn, n, level, len(src), len(got))
				}
				// Empty (but non-nil) dict and nil dict are equivalent.
				alt := dict
				if alt == nil {
					alt = []byte{}
				}
				if got2, err := Decode(nil, enc, alt); err != nil || !bytes.Equal(got2, src) {
					t.Fatalf("nil/empty dict must be equivalent, got err=%v", err)
				}
			}
		}
	}
}

func TestEmptyDataStillRequiresMatchingDict(t *testing.T) {
	// Encoded with a non-empty dictionary.
	enc, err := Encode(nil, nil, corpus, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(nil, enc, nil); !errors.Is(err, ErrDictMismatch) {
		t.Fatalf("empty data with wrong dict: want ErrDictMismatch, got %v", err)
	}
	got, err := Decode(nil, enc, corpus)
	if err != nil {
		t.Fatalf("empty data with matching dict: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want zero-length result, got %d", len(got))
	}

	// Encoded with no dictionary: a wrong (non-empty) dict must still fail.
	encNil, err := Encode(nil, []byte{}, nil, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(nil, encNil, corpus); !errors.Is(err, ErrDictMismatch) {
		t.Fatalf("nil-dict data decoded with a dict: want ErrDictMismatch, got %v", err)
	}
	if _, err := Decode(nil, encNil, nil); err != nil {
		t.Fatalf("nil-dict empty data: unexpected error %v", err)
	}
}

func TestWrongDictRejectedBeforeInflate(t *testing.T) {
	src := payload(20)
	enc, err := Encode(nil, src, corpus, flate.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	// Any different dictionary must be rejected at the header stage.
	other := []byte("a completely different dictionary, nothing alike")
	_, err = Decode(nil, enc, other)
	if !errors.Is(err, ErrDictMismatch) {
		t.Fatalf("want ErrDictMismatch, got %v", err)
	}
	// Corrupting the stored digest must also be a mismatch, never a decode.
	bad := bytes.Clone(enc)
	bad[digestOff] ^= 0xFF
	if _, err := Decode(nil, bad, corpus); !errors.Is(err, ErrDictMismatch) {
		t.Fatalf("corrupt digest: want ErrDictMismatch, got %v", err)
	}
}

func TestShortHeader(t *testing.T) {
	enc, err := Encode(nil, payload(4), corpus, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < headerSize; n++ {
		if _, err := Decode(nil, enc[:n], corpus); !errors.Is(err, ErrShortHeader) {
			t.Fatalf("len=%d: want ErrShortHeader, got %v", n, err)
		}
	}
	// Truncated after a valid header (deflate stream cut short) must fail,
	// not deliver partial data.
	if _, err := Decode(nil, enc[:headerSize], corpus); err == nil {
		t.Fatalf("truncated deflate stream unexpectedly succeeded")
	}
	if _, err := Decode(nil, enc[:headerSize+1], corpus); err == nil {
		t.Fatalf("truncated deflate stream unexpectedly succeeded")
	}
}

func TestBadMagicVersionAndReserved(t *testing.T) {
	enc, err := Encode(nil, payload(3), corpus, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	for _, pos := range []int{0, 1, 2} {
		bad := bytes.Clone(enc)
		bad[pos] ^= 0xFF
		if _, err := Decode(nil, bad, corpus); !errors.Is(err, ErrMagic) {
			t.Fatalf("flip byte %d: want ErrMagic, got %v", pos, err)
		}
	}
	bad := bytes.Clone(enc)
	bad[reserveSlot] = 1
	if _, err := Decode(nil, bad, corpus); !errors.Is(err, ErrReserved) {
		t.Fatalf("reserved byte: want ErrReserved, got %v", err)
	}
}

func TestTrailingDataRejected(t *testing.T) {
	enc, err := Encode(nil, payload(6), corpus, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	for _, extra := range [][]byte{{0x00}, {0xff}, bytes.Repeat([]byte{0x00}, 16)} {
		bad := append(bytes.Clone(enc), extra...)
		if _, err := Decode(nil, bad, corpus); !errors.Is(err, ErrTrailingData) {
			t.Fatalf("%d trailing bytes: want ErrTrailingData, got %v", len(extra), err)
		}
	}
}

func TestLengthAndChecksumTampering(t *testing.T) {
	src := payload(8)
	enc, err := Encode(nil, src, corpus, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}

	// Flip a declared-length bit. Real output cannot match it.
	bad := bytes.Clone(enc)
	bad[lengthOff] ^= 0x01
	if _, err := Decode(nil, bad, corpus); err == nil {
		t.Fatalf("tampered length unexpectedly succeeded")
	} else if !errors.Is(err, ErrLength) && !errors.Is(err, ErrChecksum) {
		// A changed length that still inflates fully must fail either the
		// length cross-check or the checksum; never succeed.
		t.Fatalf("tampered length: want ErrLength/ErrChecksum, got %v", err)
	}

	// Flip a checksum bit (and every checksum byte in turn).
	for i := crcOff; i < crcOff+4; i++ {
		bad := bytes.Clone(enc)
		bad[i] ^= 0x80
		if _, err := Decode(nil, bad, corpus); !errors.Is(err, ErrChecksum) {
			t.Fatalf("flip crc byte %d: want ErrChecksum, got %v", i, err)
		}
	}

	// Flip a payload byte. The deflate stream will error or, if it still
	// inflates, the checksum must fail; a successful decode is impossible.
	bad = bytes.Clone(enc)
	bad[len(bad)-1] ^= 0x01
	if _, err := Decode(nil, bad, corpus); err == nil {
		t.Fatalf("corrupt payload unexpectedly succeeded")
	}
}

func TestHugeDeclaredLengthDoesNotAllocate(t *testing.T) {
	// Take a valid tiny stream and rewrite its declared length to something
	// enormous. Decoding must reject it without allocating that much memory.
	tiny := []byte("hello")
	enc, err := Encode(nil, tiny, nil, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	bad := bytes.Clone(enc)
	binary.BigEndian.PutUint64(bad[lengthOff:], 1<<40)

	runtime.GC()
	var mBefore, mAfter runtime.MemStats
	runtime.ReadMemStats(&mBefore)
	_, err = Decode(nil, bad, nil)
	runtime.ReadMemStats(&mAfter)
	if !errors.Is(err, ErrLength) {
		t.Fatalf("huge declared length: want ErrLength, got %v", err)
	}
	if heap := int64(mAfter.TotalAlloc - mBefore.TotalAlloc); heap > 4<<20 {
		t.Fatalf("decode allocated %d bytes for a 1 TiB declared length", heap)
	}

	// Declared length of MaxUint64 must also be refused cheaply.
	bad2 := bytes.Clone(enc)
	binary.BigEndian.PutUint64(bad2[lengthOff:], ^uint64(0))
	if _, err := Decode(nil, bad2, nil); !errors.Is(err, ErrLength) {
		t.Fatalf("max declared length: want ErrLength, got %v", err)
	}
}

func TestInflateExceedsDeclaredLength(t *testing.T) {
	// Build an inflated stream longer than the length recorded in the
	// header: encode the data, then shrink the header length field.
	src := payload(30)
	enc, err := Encode(nil, src, corpus, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	bad := bytes.Clone(enc)
	binary.BigEndian.PutUint64(bad[lengthOff:], uint64(len(src)-1))
	if _, err := Decode(nil, bad, corpus); !errors.Is(err, ErrLength) {
		t.Fatalf("overshooting stream: want ErrLength, got %v", err)
	}
}

func TestBadLevel(t *testing.T) {
	if _, err := Encode(nil, []byte("x"), nil, 99); err == nil {
		t.Fatal("invalid level unexpectedly accepted")
	}
}

func TestAppendToDst(t *testing.T) {
	src := payload(5)
	prefix := []byte("PREFIX-")
	enc, err := Encode(nil, src, corpus, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	dst := bytes.Clone(prefix)
	got, err := Decode(dst, enc, corpus)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(got, prefix) || !bytes.Equal(got[len(prefix):], src) {
		t.Fatalf("result does not preserve dst prefix and data")
	}
}

// TestPayloadIsRawDeflate proves the DEFLATE entry/format is unchanged: the
// bytes after the header inflate with the original flate.NewReaderDict.
func TestPayloadIsRawDeflate(t *testing.T) {
	src := payload(7)
	enc, err := Encode(nil, src, corpus, flate.BestSpeed)
	if err != nil {
		t.Fatal(err)
	}
	r := flate.NewReaderDict(bytes.NewReader(enc[headerSize:]), corpus)
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("header payload is not a standard raw DEFLATE stream")
	}
}
