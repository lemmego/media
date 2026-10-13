package media

import (
	"image"
	"image/color"
	imagedraw "image/draw"
	"math"

	xdraw "golang.org/x/image/draw"

	"github.com/lemmego/media/exif"
)

// applyOrientation turns the pixels the way the EXIF tag asks for.
//
// It runs before anything else derives from the image — before the alpha
// scan, before scaling — because every later step reasons about width and
// height, and four of the eight orientations exchange them. A thumbnail box
// computed from the header dimensions of a portrait photograph is a landscape
// box, which is the bug you see as letterboxing on exactly the photographs
// people take most.
func applyOrientation(src image.Image, orientation exif.Orientation) image.Image {
	if orientation == exif.None || !orientation.Valid() {
		return src
	}

	// Normalised to NRGBA once, so the transform below is slice indexing
	// rather than sixteen million trips through the color.Color interface.
	// draw.Draw has fast paths for the usual source types; At/Set has none.
	base := toNRGBA(src)
	width, height := base.Rect.Dx(), base.Rect.Dy()

	targetWidth, targetHeight := width, height
	if orientation.SwapsDimensions() {
		targetWidth, targetHeight = height, width
	}
	dst := image.NewNRGBA(image.Rect(0, 0, targetWidth, targetHeight))

	for y := 0; y < height; y++ {
		row := base.Pix[y*base.Stride:]
		for x := 0; x < width; x++ {
			// Where this source pixel lands. The cases are the TIFF
			// orientations; each is the position of the source's first row
			// and column in the displayed image.
			var nx, ny int
			switch orientation {
			case 2: // mirrored horizontally
				nx, ny = width-1-x, y
			case 3: // rotated 180°
				nx, ny = width-1-x, height-1-y
			case 4: // mirrored vertically
				nx, ny = x, height-1-y
			case 5: // transposed along the main diagonal
				nx, ny = y, x
			case 6: // rotated 90° clockwise
				nx, ny = height-1-y, x
			case 7: // transposed along the anti-diagonal
				nx, ny = height-1-y, width-1-x
			case 8: // rotated 270° clockwise
				nx, ny = y, width-1-x
			default:
				nx, ny = x, y
			}
			source := x * 4
			target := ny*dst.Stride + nx*4
			copy(dst.Pix[target:target+4], row[source:source+4])
		}
	}
	return dst
}

// toNRGBA returns src as an *image.NRGBA with its origin at zero.
func toNRGBA(src image.Image) *image.NRGBA {
	bounds := src.Bounds()
	if already, ok := src.(*image.NRGBA); ok && bounds.Min == (image.Point{}) {
		return already
	}
	dst := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	imagedraw.Draw(dst, dst.Bounds(), src, bounds.Min, imagedraw.Src)
	return dst
}

// resize produces the rendition one conversion asks for.
//
// Never upscales. A 32-pixel avatar asked for a 256-pixel variant stays 32
// pixels: enlarging it adds bytes and no detail, and the markup reports the
// real size so the page scales it in CSS if it wants to. That rule is why
// every caller must read the returned dimensions rather than assuming the
// ones it asked for.
func resize(src image.Image, conversion Conversion) image.Image {
	bounds := src.Bounds()
	sourceWidth, sourceHeight := bounds.Dx(), bounds.Dy()
	if sourceWidth == 0 || sourceHeight == 0 {
		return src
	}

	if conversion.Fit == FitCover && conversion.Width > 0 && conversion.Height > 0 {
		return cover(src, conversion.Width, conversion.Height)
	}
	return inside(src, conversion.Width, conversion.Height)
}

// inside scales the image to sit within the box, cropping nothing.
func inside(src image.Image, width, height int) image.Image {
	bounds := src.Bounds()
	sourceWidth, sourceHeight := bounds.Dx(), bounds.Dy()

	// A zero dimension means "whatever the aspect ratio gives", so it must
	// not constrain the scale. Positive infinity is the identity for min.
	ratio := math.Inf(1)
	if width > 0 {
		ratio = math.Min(ratio, float64(width)/float64(sourceWidth))
	}
	if height > 0 {
		ratio = math.Min(ratio, float64(height)/float64(sourceHeight))
	}
	// Neither given, or the source is already smaller than the box.
	if math.IsInf(ratio, 1) || ratio >= 1 {
		return src
	}

	targetWidth := atLeastOne(int(math.Round(float64(sourceWidth) * ratio)))
	targetHeight := atLeastOne(int(math.Round(float64(sourceHeight) * ratio)))

	dst := image.NewNRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	// CatmullRom rather than ApproxBiLinear: this runs once per upload and
	// the result is served for the life of the post, so the few milliseconds
	// buy a sharper image forever. Its kernel widens with the scale factor,
	// so a large reduction is properly resampled instead of point-sampled
	// into aliasing.
	//
	// draw.Src rather than draw.Over, because the destination starts
	// transparent and the source's own alpha is what should survive —
	// compositing over transparent black darkens every translucent edge.
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, bounds, xdraw.Src, nil)
	return dst
}

// cover fills the box exactly and centre-crops whatever does not fit.
func cover(src image.Image, width, height int) image.Image {
	bounds := src.Bounds()
	sourceWidth, sourceHeight := bounds.Dx(), bounds.Dy()

	targetWidth, targetHeight := width, height

	// The no-upscale rule, applied to the box rather than to the image: if
	// the source cannot fill the requested box, shrink the box, preserving
	// the aspect ratio that was asked for. So a 40×40 avatar slot given a
	// 30×50 photograph yields 30×30 — the requested shape, at the size the
	// source can actually supply.
	if sourceWidth < targetWidth || sourceHeight < targetHeight {
		shrink := math.Min(
			float64(sourceWidth)/float64(targetWidth),
			float64(sourceHeight)/float64(targetHeight))
		targetWidth = atLeastOne(int(math.Round(float64(targetWidth) * shrink)))
		targetHeight = atLeastOne(int(math.Round(float64(targetHeight) * shrink)))
	}

	// The largest region of the source with the target's aspect ratio,
	// centred.
	aspect := float64(targetWidth) / float64(targetHeight)
	cropWidth, cropHeight := sourceWidth, sourceHeight
	if float64(sourceWidth)/float64(sourceHeight) > aspect {
		cropWidth = atLeastOne(int(math.Round(float64(sourceHeight) * aspect)))
	} else {
		cropHeight = atLeastOne(int(math.Round(float64(sourceWidth) / aspect)))
	}
	crop := image.Rect(0, 0, cropWidth, cropHeight).
		Add(bounds.Min).
		Add(image.Pt((sourceWidth-cropWidth)/2, (sourceHeight-cropHeight)/2))

	dst := image.NewNRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, crop, xdraw.Src, nil)
	return dst
}

// transparent reports whether any pixel is not fully opaque.
//
// Not "does the colour model have an alpha channel", which is the test that
// looks equivalent and is not: most PNGs are RGBA with every pixel opaque, so
// that test keeps a screenshot as a PNG several times larger than the JPEG it
// could have been.
func transparent(img image.Image) bool {
	// Every concrete type in image/ implements this, and each knows the
	// cheapest answer for its own storage — YCbCr returns true outright.
	if opaque, ok := img.(interface{ Opaque() bool }); ok {
		return !opaque.Opaque()
	}
	bounds := img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if _, _, _, alpha := img.At(x, y).RGBA(); alpha != 0xFFFF {
				return true
			}
		}
	}
	return false
}

// flatten composites the image onto white.
//
// Required before a JPEG encode, because JPEG has no alpha channel and the
// encoder reads the colour values as they are — so a transparent pixel, which
// is stored as transparent *black*, is written as black. A logo with a
// transparent background becomes a logo on a black rectangle. White is the
// right default because a page is white; black is what you get by accident.
func flatten(img image.Image) image.Image {
	bounds := img.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	imagedraw.Draw(dst, dst.Bounds(), image.NewUniform(color.White),
		image.Point{}, imagedraw.Src)
	imagedraw.Draw(dst, dst.Bounds(), img, bounds.Min, imagedraw.Over)
	return dst
}

func atLeastOne(n int) int {
	if n < 1 {
		return 1
	}
	return n
}
