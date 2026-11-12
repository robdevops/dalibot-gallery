package imaging

import (
	"image"
	"os"
	"testing"

	"github.com/davidbyttow/govips/v2/vips"
)

// TestWritePlaceholder regenerates placeholder.webp (a 16x16 lossless WebP in
// Background colour) with the same libvips the pipeline uses:
//
//	GALLERY_WRITE_PLACEHOLDER=1 go test ./internal/imaging -run TestWritePlaceholder
//
// TestPlaceholder checks the committed file pixel by pixel.
func TestWritePlaceholder(t *testing.T) {
	if os.Getenv("GALLERY_WRITE_PLACEHOLDER") == "" {
		t.Skip("set GALLERY_WRITE_PLACEHOLDER=1 to regenerate placeholder.webp")
	}
	img := image.NewGray(image.Rect(0, 0, 16, 16))
	for i := range img.Pix {
		img.Pix[i] = 0x11
	}
	ref, err := vips.NewImageFromBuffer(encodeJPEG(t, img))
	if err != nil {
		t.Fatal(err)
	}
	defer ref.Close()
	if err := ref.ToColorSpace(vips.InterpretationSRGB); err != nil {
		t.Fatal(err)
	}
	out, _, err := ref.ExportWebp(&vips.WebpExportParams{Lossless: true, StripMetadata: true, ReductionEffort: 6, Quality: 100})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("placeholder.webp", out, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote placeholder.webp, %d bytes", len(out))
}
