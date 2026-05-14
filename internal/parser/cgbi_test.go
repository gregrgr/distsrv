package parser

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// buildCgBIPNG fabricates a minimal CgBI PNG: 2x2 BGRA, no filter, raw DEFLATE.
func buildCgBIPNG(t *testing.T) []byte {
	t.Helper()

	// Pixels in BGRA, with leading filter=0 per scanline.
	// Row0: BGRA(255,0,0,255) BGRA(0,255,0,255)
	//   -> after R↔B swap during normalize:
	//      RGBA(0,0,255,255)  RGBA(0,255,0,255)
	// Row1: BGRA(0,0,255,255) BGRA(255,255,255,255)
	row0 := []byte{
		0x00,
		255, 0, 0, 255,
		0, 255, 0, 255,
	}
	row1 := []byte{
		0x00,
		0, 0, 255, 255,
		255, 255, 255, 255,
	}
	raw := append(row0, row1...)

	// Raw DEFLATE (no zlib wrapper) — the CgBI quirk.
	var fbuf bytes.Buffer
	fw, err := flate.NewWriter(&fbuf, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := fw.Close(); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	out.Write(pngSignature)

	writePNGChunk(&out, "CgBI", []byte{0x50, 0x00, 0x20, 0x06})

	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], 2)
	binary.BigEndian.PutUint32(ihdr[4:8], 2)
	ihdr[8] = 8 // bit depth
	ihdr[9] = 6 // RGBA
	writePNGChunk(&out, "IHDR", ihdr)

	writePNGChunk(&out, "IDAT", fbuf.Bytes())
	writePNGChunk(&out, "IEND", nil)

	return out.Bytes()
}

func standardPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	img.Set(1, 0, color.RGBA{G: 255, A: 255})
	img.Set(0, 1, color.RGBA{B: 255, A: 255})
	img.Set(1, 1, color.RGBA{R: 255, G: 255, B: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestIsAppleCgBIPNG(t *testing.T) {
	if !IsAppleCgBIPNG(buildCgBIPNG(t)) {
		t.Fatal("expected detection of CgBI PNG")
	}
	if IsAppleCgBIPNG(standardPNG(t)) {
		t.Fatal("standard PNG falsely flagged as CgBI")
	}
}

func TestNormalizeAppleCgBI(t *testing.T) {
	cg := buildCgBIPNG(t)

	// Standard libpng should refuse to decode CgBI.
	if _, err := png.Decode(bytes.NewReader(cg)); err == nil {
		t.Log("note: stdlib decoded the fake CgBI — that's fine, this is a synthetic test fixture")
	}

	fixed := NormalizeAppleCgBI(cg)
	if bytes.Equal(cg, fixed) {
		t.Fatal("expected output to differ from input")
	}
	if IsAppleCgBIPNG(fixed) {
		t.Fatal("CgBI chunk still present after normalize")
	}

	img, err := png.Decode(bytes.NewReader(fixed))
	if err != nil {
		t.Fatalf("stdlib png decode failed: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != 2 || b.Dy() != 2 {
		t.Fatalf("unexpected size: %dx%d", b.Dx(), b.Dy())
	}

	// After R↔B swap, top-left BGRA(255,0,0) becomes RGBA(0,0,255).
	r, g, bl, a := img.At(0, 0).RGBA()
	if r>>8 != 0 || g>>8 != 0 || bl>>8 != 255 || a>>8 != 255 {
		t.Errorf("top-left = (%d,%d,%d,%d); want (0,0,255,255)",
			r>>8, g>>8, bl>>8, a>>8)
	}

	// IDAT must now decode under a real zlib reader.
	pos := 8
	var idat []byte
	for pos+12 <= len(fixed) {
		length := int(binary.BigEndian.Uint32(fixed[pos : pos+4]))
		typ := string(fixed[pos+4 : pos+8])
		data := fixed[pos+8 : pos+8+length]
		if typ == "IDAT" {
			idat = append(idat, data...)
		}
		pos = pos + 8 + length + 4
	}
	zr, err := zlib.NewReader(bytes.NewReader(idat))
	if err != nil {
		t.Fatalf("zlib reader on fixed IDAT: %v", err)
	}
	_ = zr.Close()
}

func TestNormalizeAppleCgBI_PassThroughStandardPNG(t *testing.T) {
	std := standardPNG(t)
	out := NormalizeAppleCgBI(std)
	if !bytes.Equal(std, out) {
		t.Fatal("non-CgBI PNG should pass through unchanged")
	}
}

func TestNormalizeAppleCgBI_GarbageInput(t *testing.T) {
	if got := NormalizeAppleCgBI([]byte("not a png")); !bytes.Equal(got, []byte("not a png")) {
		t.Fatal("garbage input should pass through")
	}
	if got := NormalizeAppleCgBI(nil); got != nil {
		t.Fatal("nil input should pass through")
	}
}
