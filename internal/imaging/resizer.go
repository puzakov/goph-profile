// Package imaging содержит утилиты работы с изображениями:
// декодирование и создание миниатюр.
package imaging

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"io"

	// Регистрация декодеров форматов для image.Decode.
	_ "golang.org/x/image/webp"
	_ "image/gif"
	_ "image/png"

	xdraw "golang.org/x/image/draw"
)

// ErrDecodeFailed — изображение не удалось декодировать (битый файл).
var ErrDecodeFailed = errors.New("image decode failed")

// Decode читает изображение из потока и возвращает декодированный образ.
// Поддерживаются форматы, зарегистрированные в image: png, jpeg, gif, webp.
func Decode(r io.Reader) (image.Image, error) {
	img, _, err := image.Decode(r)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDecodeFailed, err)
	}
	return img, nil
}

// ResizeToJPEG создаёт квадратную миниатюру width x height в формате JPEG:
// центральная область исходного изображения обрезается до квадрата
// и масштабируется. Прозрачные области заливаются белым (у JPEG нет альфы).
func ResizeToJPEG(img image.Image, width, height int, quality int) ([]byte, error) {
	src := squareCrop(img)

	// Сначала рисуем на белый холст: полупрозрачные пиксели смешаются
	// с белым, а не с чёрным.
	opaque := image.NewRGBA(src.Bounds())
	draw.Draw(opaque, opaque.Bounds(), image.White, image.Point{}, draw.Src)
	draw.Draw(opaque, opaque.Bounds(), src, image.Point{}, draw.Over)

	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	xdraw.BiLinear.Scale(dst, dst.Bounds(), opaque, opaque.Bounds(), xdraw.Src, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: quality}); err != nil {
		return nil, fmt.Errorf("encode jpeg: %w", err)
	}
	return buf.Bytes(), nil
}

// squareCrop вырезает из изображения максимальный центральный квадрат.
func squareCrop(img image.Image) image.Image {
	b := img.Bounds()
	side := min(b.Dx(), b.Dy())
	if side == b.Dx() && side == b.Dy() {
		return img
	}
	x0 := b.Min.X + (b.Dx()-side)/2
	y0 := b.Min.Y + (b.Dy()-side)/2
	crop := image.Rect(x0, y0, x0+side, y0+side)

	dst := image.NewRGBA(image.Rect(0, 0, side, side))
	draw.Draw(dst, dst.Bounds(), img, crop.Min, draw.Src)
	return dst
}
