package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
)

var iconSizes = []int{16, 24, 32, 48, 64, 128, 256}

func main() {
	entries := make([]icoEntry, 0, len(iconSizes))
	for _, size := range iconSizes {
		icon := render(size)
		data, err := encodePNG(icon)
		if err != nil {
			log.Fatal(err)
		}
		entries = append(entries, icoEntry{size: size, data: data})
		if size == 256 {
			mustWritePNG(filepath.Join("assets", "portpilot.png"), icon)
		}
	}
	ico := encodeICO(entries)
	for _, path := range []string{
		filepath.Join("assets", "portpilot.ico"),
		filepath.Join("internal", "app", "portpilot.ico"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			log.Fatal(err)
		}
		if err := os.WriteFile(path, ico, 0644); err != nil {
			log.Fatal(err)
		}
	}
}

type icoEntry struct {
	size int
	data []byte
}

func render(size int) image.Image {
	scale := 4
	highResolution := image.NewNRGBA(image.Rect(0, 0, size*scale, size*scale))
	s := float64(size * scale)
	for y := 0; y < highResolution.Bounds().Dy(); y++ {
		for x := 0; x < highResolution.Bounds().Dx(); x++ {
			xf, yf := float64(x)+0.5, float64(y)+0.5
			if roundedRect(xf, yf, s*0.06, s*0.06, s*0.88, s*0.88, s*0.20) {
				t := yf / s
				highResolution.SetNRGBA(x, y, color.NRGBA{R: uint8(18 + 10*t), G: uint8(104 + 55*t), B: uint8(139 + 35*t), A: 255})
			}
		}
	}
	paint(highResolution, color.NRGBA{R: 255, G: 255, B: 255, A: 245}, func(x, y float64) bool {
		return circle(x, y, s*0.42, s*0.50, s*0.235)
	})
	paint(highResolution, color.NRGBA{R: 21, G: 122, B: 151, A: 255}, func(x, y float64) bool {
		return circle(x, y, s*0.42, s*0.50, s*0.075) ||
			circle(x, y, s*0.31, s*0.39, s*0.035) || circle(x, y, s*0.53, s*0.39, s*0.035) ||
			circle(x, y, s*0.31, s*0.61, s*0.035) || circle(x, y, s*0.53, s*0.61, s*0.035)
	})
	paint(highResolution, color.NRGBA{R: 229, G: 249, B: 245, A: 255}, func(x, y float64) bool {
		return roundedRect(x, y, s*0.58, s*0.45, s*0.20, s*0.10, s*0.05) ||
			triangle(x, y, s*0.70, s*0.35, s*0.86, s*0.50, s*0.70, s*0.65)
	})
	return downsample(highResolution, size)
}

func paint(img *image.NRGBA, value color.NRGBA, contains func(float64, float64) bool) {
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			if contains(float64(x)+0.5, float64(y)+0.5) {
				img.SetNRGBA(x, y, value)
			}
		}
	}
}

func roundedRect(x, y, left, top, width, height, radius float64) bool {
	right, bottom := left+width, top+height
	if x < left || x > right || y < top || y > bottom {
		return false
	}
	cx := math.Max(left+radius, math.Min(x, right-radius))
	cy := math.Max(top+radius, math.Min(y, bottom-radius))
	return math.Hypot(x-cx, y-cy) <= radius
}

func circle(x, y, cx, cy, radius float64) bool {
	return math.Hypot(x-cx, y-cy) <= radius
}

func triangle(px, py, ax, ay, bx, by, cx, cy float64) bool {
	d1 := sign(px, py, ax, ay, bx, by)
	d2 := sign(px, py, bx, by, cx, cy)
	d3 := sign(px, py, cx, cy, ax, ay)
	hasNegative := d1 < 0 || d2 < 0 || d3 < 0
	hasPositive := d1 > 0 || d2 > 0 || d3 > 0
	return !(hasNegative && hasPositive)
}

func sign(px, py, ax, ay, bx, by float64) float64 {
	return (px-bx)*(ay-by) - (ax-bx)*(py-by)
}

func downsample(source *image.NRGBA, size int) *image.NRGBA {
	destination := image.NewNRGBA(image.Rect(0, 0, size, size))
	factor := source.Bounds().Dx() / size
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var red, green, blue, alpha uint32
			for yy := 0; yy < factor; yy++ {
				for xx := 0; xx < factor; xx++ {
					pixel := source.NRGBAAt(x*factor+xx, y*factor+yy)
					red += uint32(pixel.R)
					green += uint32(pixel.G)
					blue += uint32(pixel.B)
					alpha += uint32(pixel.A)
				}
			}
			count := uint32(factor * factor)
			destination.SetNRGBA(x, y, color.NRGBA{R: uint8(red / count), G: uint8(green / count), B: uint8(blue / count), A: uint8(alpha / count)})
		}
	}
	return destination
}

func encodePNG(img image.Image) ([]byte, error) {
	var buffer bytes.Buffer
	err := png.Encode(&buffer, img)
	return buffer.Bytes(), err
}

func mustWritePNG(path string, img image.Image) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		log.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()
	if err := png.Encode(file, img); err != nil {
		log.Fatal(err)
	}
}

func encodeICO(entries []icoEntry) []byte {
	var buffer bytes.Buffer
	_ = binary.Write(&buffer, binary.LittleEndian, uint16(0))
	_ = binary.Write(&buffer, binary.LittleEndian, uint16(1))
	_ = binary.Write(&buffer, binary.LittleEndian, uint16(len(entries)))
	offset := 6 + len(entries)*16
	for _, entry := range entries {
		dimension := byte(entry.size)
		if entry.size == 256 {
			dimension = 0
		}
		buffer.WriteByte(dimension)
		buffer.WriteByte(dimension)
		buffer.WriteByte(0)
		buffer.WriteByte(0)
		_ = binary.Write(&buffer, binary.LittleEndian, uint16(1))
		_ = binary.Write(&buffer, binary.LittleEndian, uint16(32))
		_ = binary.Write(&buffer, binary.LittleEndian, uint32(len(entry.data)))
		_ = binary.Write(&buffer, binary.LittleEndian, uint32(offset))
		offset += len(entry.data)
	}
	for _, entry := range entries {
		buffer.Write(entry.data)
	}
	return buffer.Bytes()
}
