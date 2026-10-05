# dictflate

`dictflate` is a dictionary-tagged compression wrapper around the existing
[`flate`](../flate) package. It keeps the raw RFC 1951 DEFLATE format and
entry points unchanged and prepends a small fixed header that records which
preset dictionary the stream was built with.

The dictionary is **not** stored in the output. The calling process supplies
it in memory for both encode and decode. On decode the header's dictionary
digest is checked first; if the supplied dictionary does not match, decoding
stops before DEFLATE is ever entered, so a wrong dictionary cannot surface
plausible-looking data.

## Usage

```go
import "github.com/klauspost/compress/dictflate"

dict := []byte("...application-specific preset dictionary...")

encoded, err := dictflate.Encode(nil, data, dict, flate.DefaultCompression)
// ...
decoded, err := dictflate.Decode(nil, encoded, dict)
```

Only a **single data segment** is supported. Exactly one raw DEFLATE member
follows the header; trailing bytes are rejected.

## Wire protocol

All multi-byte integer fields are unsigned big-endian. The header is exactly
24 bytes, immediately followed by one raw DEFLATE stream (RFC 1951) produced
by `flate.NewWriterDict` with the recorded dictionary.

| Offset | Size | Field          | Value / notes                                                        |
|-------:|-----:|----------------|----------------------------------------------------------------------|
| 0      | 1    | magic0         | `'D'` (`0x44`)                                                       |
| 1      | 1    | magic1         | `'F'` (`0x46`)                                                       |
| 2      | 1    | version        | `1`                                                                  |
| 3      | 1    | reserved       | must be `0`; any other value rejects the frame                       |
| 4      | 8    | dict digest    | FNV-1a 64-bit digest of the preset dictionary (`fnv.New64a`)         |
| 12     | 8    | original length| byte length of the uncompressed data                                 |
| 20     | 4    | data CRC-32C   | IEEE CRC-32 (Castagnoli) checksum of the uncompressed data           |
| 24     | var  | deflate stream | one raw DEFLATE member, no trailer, no extra members                 |

Notes:

- A `nil` dictionary and an empty (`[]byte{}`) dictionary share the same
  digest and are interchangeable.
- Empty data is legal: it produces a valid frame with `original length = 0`,
  but the dictionary digest is still verified.
- The dictionary digest matches before inflation; length and CRC-32C are
  checked after inflation, and the frame must end exactly at the DEFLATE
  stream's last byte.

## Decoder rejection rules

| Condition                                   | Result            |
|--------------------------------------------|-------------------|
| Fewer than 24 bytes                        | `ErrShortHeader`  |
| Bad magic or unsupported version           | `ErrMagic`        |
| Non-zero reserved byte                     | `ErrReserved`     |
| Supplied dictionary digest differs         | `ErrDictMismatch` (before DEFLATE) |
| Bytes after the DEFLATE segment            | `ErrTrailingData` |
| Inflated length differs from the header     | `ErrLength`       |
| Declared length above `MaxInt32`           | `ErrLength`       |
| Inflated CRC-32C differs from the header    | `ErrChecksum`     |
| Truncated/corrupt DEFLATE stream           | DEFLATE error     |

A declared length that is much larger than the real data never causes a
matching allocation: output grows lazily (normal append growth) bounded by
the declared length, and overshoot is an `ErrLength`. Corrupted input never
returns a successful result.

## Tests

```sh
go test ./dictflate/ -v
go test ./dictflate/ -run 'TestRoundTrip|TestWrongDict|TestHugeDeclaredLength' -v
```
