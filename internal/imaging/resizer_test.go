package imaging

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

// testPNG — PNG 1x1 (прозрачный пиксель).
var testPNG = decodeB64("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")

func decodeB64(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// pngImage создаёт PNG-изображение заданного размера с полупрозрачным заполнением.
func pngImage(t *testing.T, w, h int, c color.Color) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetNRGBA(x, y, color.NRGBAModel.Convert(c).(color.NRGBA))
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDecode_ValidPNG(t *testing.T) {
	img, err := Decode(bytes.NewReader(testPNG))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 1 || b.Dy() != 1 {
		t.Errorf("bounds = %v, want 1x1", b)
	}
}

func TestDecode_InvalidData(t *testing.T) {
	if _, err := Decode(strings.NewReader("not an image")); err == nil {
		t.Fatal("expected error for invalid image data")
	}
}

func TestResizeToJPEG_ProducesJPEGOfRequestedSize(t *testing.T) {
	src, err := Decode(bytes.NewReader(pngImage(t, 200, 100, color.NRGBA{R: 200, G: 100, B: 50, A: 255})))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	data, err := ResizeToJPEG(src, 100, 100, 85)
	if err != nil {
		t.Fatalf("ResizeToJPEG: %v", err)
	}

	if !bytes.HasPrefix(data, []byte{0xFF, 0xD8}) {
		t.Error("result is not a JPEG (magic bytes 0xFFD8 expected)")
	}
	img, err := Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 100 || b.Dy() != 100 {
		t.Errorf("result size = %v, want 100x100", b)
	}
}

func TestResizeToJPEG_CropsToSquare(t *testing.T) {
	// Исходник 400x100: центральный квадрат 100x100 — должны получить ровно этот цвет.
	src, err := Decode(bytes.NewReader(pngImage(t, 400, 100, color.NRGBA{R: 10, G: 200, B: 30, A: 255})))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	data, err := ResizeToJPEG(src, 100, 100, 95)
	if err != nil {
		t.Fatalf("ResizeToJPEG: %v", err)
	}
	img, err := Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 100 || b.Dy() != 100 {
		t.Fatalf("result size = %v, want 100x100", b)
	}
	// Центр результата должен быть зелёным.
	c := img.At(50, 50)
	r, g, bb, _ := c.RGBA()
	if r>>8 > 60 || g>>8 < 140 || bb>>8 > 80 {
		t.Errorf("center pixel = %v, want greenish (crop of central square)", c)
	}
}

func TestResizeToJPEG_WhiteBackgroundForTransparent(t *testing.T) {
	// Полностью прозрачный PNG: JPEG-миниатюра должна иметь белый фон, а не чёрный.
	src, err := Decode(bytes.NewReader(pngImage(t, 100, 100, color.NRGBA{R: 0, G: 0, B: 0, A: 0})))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	data, err := ResizeToJPEG(src, 50, 50, 90)
	if err != nil {
		t.Fatalf("ResizeToJPEG: %v", err)
	}
	img, err := Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode result: %v", err)
	}
	r, g, b, _ := img.At(25, 25).RGBA()
	if r>>8 < 240 || g>>8 < 240 || b>>8 < 240 {
		t.Errorf("pixel = (%d,%d,%d), want white background", r>>8, g>>8, b>>8)
	}
}
