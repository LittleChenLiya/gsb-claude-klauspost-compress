# dictflate

A dictionary-aware DEFLATE compression wrapper.

The format prepends a small fixed-size header containing a version byte, a
digest of the dictionary, and the original uncompressed length. Decompression
verifies the dictionary digest before invoking the underlying DEFLATE decoder,
so a mismatched dictionary is rejected before any flate processing occurs and
cannot produce seemingly valid output.

The underlying DEFLATE format is not modified; this package uses the standard
`flate.NewWriterDict` / `flate.NewReaderDict` entry points. Only a single
contiguous data segment is supported.

## Protocol

All integers are little-endian.

| Offset | Size | Field      | Description                              |
|-------:|-----:|------------|------------------------------------------|
| 0      | 1    | version    | Format version; must be `1`.             |
| 1      | 8    | dictDigest | FNV-1a 64-bit digest of the dictionary. |
| 9      | 8    | origLen    | Original uncompressed length (uint64).   |
| 17     | ...  | payload    | DEFLATE compressed stream (RFC 1951).    |

The header is followed by exactly one DEFLATE stream. No trailing bytes are
permitted. The dictionary is supplied by the caller in-process and is never
stored in the frame.

### Header fields

- **version** — format version. Currently always `1`. Any other value is
  rejected with `ErrBadVersion`.
- **dictDigest** — FNV-1a 64-bit digest of the dictionary. This is verified
  before any DEFLATE processing; a mismatch returns `ErrDictMismatch`.
- **origLen** — the original uncompressed length in bytes. If this exceeds the
  configured maximum, `ErrTooLarge` is returned without allocating a large
  buffer. After decompression, the actual length must equal this value
  (`ErrLengthMismatch` otherwise).

### Payload

The payload is a single DEFLATE stream compressed with the dictionary. After
the stream, no trailing bytes are permitted (`ErrTrailingData` otherwise).

## Errors

| Error                | Description                                              |
|----------------------|----------------------------------------------------------|
| `ErrShortHeader`     | Input is shorter than the 17-byte header.                |
| `ErrBadVersion`      | Header version is not `1`.                               |
| `ErrDictMismatch`    | Dictionary digest does not match (checked before flate).|
| `ErrTooLarge`        | Declared or actual size exceeds the configured maximum. |
| `ErrLengthMismatch`  | Decompressed length does not match the declared length.  |
| `ErrTrailingData`    | Bytes follow the compressed stream.                      |
| `ErrCorrupt`         | Compressed data is corrupt.                              |

## Usage

```go
import "github.com/klauspost/compress/dictflate"

dict := []byte("my dictionary")
data := []byte("hello world")

// Compress.
enc, err := dictflate.Encode(dict, data, flate.DefaultCompression)

// Decompress. The dictionary digest is verified before flate.
dec, err := dictflate.Decode(dict, enc)
```

## Tests

Run the tests with:

```sh
go test ./dictflate/ -v -count=1
```

The tests verify:

- Correct dictionary round-trips the same bytes (at all compression levels).
- A wrong dictionary is rejected before entering flate (`ErrDictMismatch`).
- A short header is rejected without allocating (`ErrShortHeader`).
- An over-long declaration is rejected without allocating a large buffer
  (`ErrTooLarge`).
- Empty data is legal but the dictionary is still verified.
- Trailing data after the compressed stream is rejected (`ErrTrailingData`).
- A bad version is rejected (`ErrBadVersion`).
- A length mismatch is rejected (`ErrLengthMismatch`).
- Corrupt compressed data is rejected.
