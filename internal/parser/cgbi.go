package parser

// Apple's iOS app icons are stored as a non-standard PNG variant called "CgBI":
//   - An extra "CgBI" chunk appears before IHDR signalling the optimization.
//   - The IDAT data is a raw DEFLATE stream (no zlib header/trailer).
//   - Pixel byte order is BGRA instead of RGBA for color type 6 (or BGR for 2).
//
// iOS Safari decodes these natively, but Chrome/Firefox/Edge use libpng which
// rejects them. NormalizeAppleCgBI rewrites such a PNG into a standards-
// compliant one so it renders in any browser.
//
// References:
//   - https://iphonedev.wiki/index.php/CgBI_file_format
//   - http://www.libpng.org/pub/png/spec/iso/index-object.html

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"encoding/binary"
	"hash/crc32"
	"io"
)

var pngSignature = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}

// NormalizeAppleCgBI converts an Apple CgBI-encoded PNG to a standard PNG.
// If the input is not a valid PNG or already standard, the input is returned
// unchanged.
func NormalizeAppleCgBI(in []byte) []byte {
	if len(in) < 8 || !bytes.Equal(in[:8], pngSignature) {
		return in
	}

	// First pass: detect CgBI, capture IHDR and concatenated IDAT.
	var (
		hasCgBI  bool
		idatBuf  []byte
		width    int
		height   int
		bitDepth byte
		colorTyp byte
	)

	pos := 8
	for pos+12 <= len(in) {
		length := int(binary.BigEndian.Uint32(in[pos : pos+4]))
		typ := string(in[pos+4 : pos+8])
		dataStart := pos + 8
		dataEnd := dataStart + length
		if dataEnd+4 > len(in) {
			return in // malformed
		}
		data := in[dataStart:dataEnd]

		switch typ {
		case "CgBI":
			hasCgBI = true
		case "IHDR":
			if len(data) < 13 {
				return in
			}
			width = int(binary.BigEndian.Uint32(data[0:4]))
			height = int(binary.BigEndian.Uint32(data[4:8]))
			bitDepth = data[8]
			colorTyp = data[9]
		case "IDAT":
			idatBuf = append(idatBuf, data...)
		case "IEND":
			// done
		}

		pos = dataEnd + 4 // CRC
	}

	if !hasCgBI {
		return in
	}

	// Only handle the common case: 8-bit, color type 6 (RGBA) or 2 (RGB).
	if bitDepth != 8 || (colorTyp != 6 && colorTyp != 2) {
		return in
	}
	bpp := 4
	if colorTyp == 2 {
		bpp = 3
	}

	// Inflate raw DEFLATE (CgBI strips the zlib wrapper).
	fr := flate.NewReader(bytes.NewReader(idatBuf))
	raw, err := io.ReadAll(fr)
	_ = fr.Close()
	if err != nil {
		return in
	}

	// Each PNG scanline: 1 filter byte + width * bpp pixel bytes.
	scanLen := 1 + width*bpp
	if len(raw) < scanLen*height {
		return in
	}

	// Apple stores pixels as BGRA / BGR; swap R↔B per pixel to get RGBA / RGB.
	for y := 0; y < height; y++ {
		row := y * scanLen
		for x := 0; x < width; x++ {
			off := row + 1 + x*bpp
			raw[off], raw[off+2] = raw[off+2], raw[off]
		}
	}

	// Re-compress with a standard zlib wrapper.
	var zbuf bytes.Buffer
	zw := zlib.NewWriter(&zbuf)
	if _, err := zw.Write(raw); err != nil {
		return in
	}
	if err := zw.Close(); err != nil {
		return in
	}
	newIDAT := zbuf.Bytes()

	// Second pass: re-emit chunks in original order, dropping CgBI and
	// replacing the (possibly multiple) IDATs with a single fixed one.
	var out bytes.Buffer
	out.Write(pngSignature)

	pos = 8
	idatEmitted := false
	for pos+12 <= len(in) {
		length := int(binary.BigEndian.Uint32(in[pos : pos+4]))
		typ := string(in[pos+4 : pos+8])
		dataStart := pos + 8
		dataEnd := dataStart + length
		data := in[dataStart:dataEnd]

		switch typ {
		case "CgBI":
			// drop
		case "IDAT":
			if !idatEmitted {
				writePNGChunk(&out, "IDAT", newIDAT)
				idatEmitted = true
			}
		default:
			writePNGChunk(&out, typ, data)
		}
		pos = dataEnd + 4
	}

	return out.Bytes()
}

func writePNGChunk(w io.Writer, typ string, data []byte) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(data)))
	_, _ = w.Write(length[:])
	_, _ = w.Write([]byte(typ))
	_, _ = w.Write(data)

	h := crc32.NewIEEE()
	_, _ = h.Write([]byte(typ))
	_, _ = h.Write(data)
	var crc [4]byte
	binary.BigEndian.PutUint32(crc[:], h.Sum32())
	_, _ = w.Write(crc[:])
}

// IsAppleCgBIPNG reports whether b looks like a CgBI-encoded PNG.
func IsAppleCgBIPNG(b []byte) bool {
	if len(b) < 8+8+4 || !bytes.Equal(b[:8], pngSignature) {
		return false
	}
	// First chunk's type starts at b[12:16].
	return string(b[12:16]) == "CgBI"
}
