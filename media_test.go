package media_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/gif"
	"strings"
	"testing"
	"time"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/lemmego/media"
	"github.com/lemmego/media/mediatest"
)

func manager(t *testing.T, store media.Store) *media.Manager {
	t.Helper()
	m, err := media.New(media.Options{Store: store})
	if err != nil {
		t.Fatalf("building the manager: %v", err)
	}
	return m
}

// dimensions decodes stored bytes, so an assertion is about the file rather
// than about what the Result claims.
func dimensions(t *testing.T, raw []byte) (int, int, string) {
	t.Helper()
	config, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("the stored bytes do not decode: %v", err)
	}
	return config.Width, config.Height, format
}

func request(conversions ...media.Conversion) media.Request {
	return media.Request{Dir: "posts", Base: "9-1760000000-0", Conversions: conversions}
}

// The reported size must be the size of the file, because the whole point of
// reporting it is that markup can reserve the space before the bytes arrive.
func TestAVariantReportsTheSizeItActuallyIs(t *testing.T) {
	store := mediatest.NewMemory()
	result, err := manager(t, store).Ingest(context.Background(),
		bytes.NewReader(mediatest.OpaqueJPEG(t, 1200, 800)),
		request(media.Conversion{Name: "sm", Width: 400, Height: 400}))
	if err != nil {
		t.Fatalf("ingesting: %v", err)
	}

	variant, ok := result.Variant("sm")
	if !ok {
		t.Fatal(`there is no variant named "sm"`)
	}
	width, height, _ := dimensions(t, store.Bytes(t, variant.Path))
	if width != variant.Width || height != variant.Height {
		t.Fatalf("the Result says %d×%d and the file is %d×%d",
			variant.Width, variant.Height, width, height)
	}
	// FitInside preserves the aspect ratio, so the long edge hits the box and
	// the short one does not.
	if width != 400 || height != 267 {
		t.Fatalf("got %d×%d, want 400×267 — a 3:2 source inside a square box", width, height)
	}
}

// Enlarging adds bytes and no detail, so it is never done — which is why a
// caller must read the returned dimensions rather than assume the ones it
// asked for.
func TestNothingIsUpscaled(t *testing.T) {
	store := mediatest.NewMemory()
	result, err := manager(t, store).Ingest(context.Background(),
		bytes.NewReader(mediatest.OpaqueJPEG(t, 32, 32)),
		request(media.Conversion{Name: "lg", Width: 256, Height: 256}))
	if err != nil {
		t.Fatalf("ingesting: %v", err)
	}

	variant, _ := result.Variant("lg")
	if variant.Width != 32 || variant.Height != 32 {
		t.Fatalf("a 32×32 source asked for 256×256 produced %d×%d",
			variant.Width, variant.Height)
	}
}

func TestCoverFillsTheBoxExactly(t *testing.T) {
	store := mediatest.NewMemory()
	result, err := manager(t, store).Ingest(context.Background(),
		bytes.NewReader(mediatest.OpaqueJPEG(t, 1200, 800)),
		request(media.Conversion{Name: "avatar", Width: 200, Height: 200, Fit: media.FitCover}))
	if err != nil {
		t.Fatalf("ingesting: %v", err)
	}

	variant, _ := result.Variant("avatar")
	width, height, _ := dimensions(t, store.Bytes(t, variant.Path))
	if width != 200 || height != 200 {
		t.Fatalf("FitCover produced %d×%d for a 200×200 box", width, height)
	}
}

// The no-upscale rule applied to the box rather than the image: the requested
// shape is kept and the size drops to what the source can supply.
func TestCoverShrinksTheBoxRatherThanEnlargingTheImage(t *testing.T) {
	store := mediatest.NewMemory()
	result, err := manager(t, store).Ingest(context.Background(),
		bytes.NewReader(mediatest.OpaqueJPEG(t, 30, 50)),
		request(media.Conversion{Name: "avatar", Width: 40, Height: 40, Fit: media.FitCover}))
	if err != nil {
		t.Fatalf("ingesting: %v", err)
	}

	variant, _ := result.Variant("avatar")
	if variant.Width != 30 || variant.Height != 30 {
		t.Fatalf("got %d×%d, want 30×30 — the square that a 30×50 source can fill",
			variant.Width, variant.Height)
	}
}

// FormatAuto decides on the pixels, not the colour model. A PNG with an
// unused alpha channel is the common case on the web and must become a JPEG.
func TestAnOpaquePNGIsWrittenAsJPEG(t *testing.T) {
	store := mediatest.NewMemory()
	result, err := manager(t, store).Ingest(context.Background(),
		bytes.NewReader(mediatest.OpaquePNG(t, 300, 300)),
		request(media.Conversion{Name: "sm", Width: 100, Height: 100}))
	if err != nil {
		t.Fatalf("ingesting: %v", err)
	}

	variant, _ := result.Variant("sm")
	if variant.Format != "jpeg" {
		t.Fatalf("an opaque PNG produced a %s variant — choosing by colour "+
			"model keeps a screenshot several times larger than it needs to be",
			variant.Format)
	}
	if !strings.HasSuffix(variant.Path, ".jpg") {
		t.Fatalf("the path %q does not match the format", variant.Path)
	}
	_, _, format := dimensions(t, store.Bytes(t, variant.Path))
	if format != "jpeg" {
		t.Fatalf("the Result says jpeg and the file is %s", format)
	}
}

func TestATranslucentPNGStaysPNG(t *testing.T) {
	store := mediatest.NewMemory()
	result, err := manager(t, store).Ingest(context.Background(),
		bytes.NewReader(mediatest.TransparentPNG(t, 300, 300)),
		request(media.Conversion{Name: "sm", Width: 100, Height: 100}))
	if err != nil {
		t.Fatalf("ingesting: %v", err)
	}
	if !result.Source.Transparent {
		t.Fatal("a PNG with a fully transparent pixel was read as opaque")
	}
	variant, _ := result.Variant("sm")
	if variant.Format != "png" {
		t.Fatalf("a translucent source produced a %s variant, which would "+
			"composite its transparency onto a background", variant.Format)
	}
}

// A JPEG asked for from a transparent source must be flattened onto white.
// Without that, transparent pixels — which are stored as transparent *black* —
// encode as black, so a logo arrives on a black rectangle.
func TestJPEGFromATransparentSourceIsFlattenedOntoWhite(t *testing.T) {
	store := mediatest.NewMemory()
	result, err := manager(t, store).Ingest(context.Background(),
		bytes.NewReader(mediatest.TransparentPNG(t, 64, 64)),
		request(media.Conversion{
			Name: "sq", Width: 64, Height: 64, Format: media.FormatJPEG,
		}))
	if err != nil {
		t.Fatalf("ingesting: %v", err)
	}

	variant, _ := result.Variant("sq")
	img, _, err := image.Decode(bytes.NewReader(store.Bytes(t, variant.Path)))
	if err != nil {
		t.Fatalf("decoding the variant: %v", err)
	}
	// The fixture's transparent pixel is at the origin.
	r, g, b, _ := img.At(0, 0).RGBA()
	if r < 0xC000 || g < 0xC000 || b < 0xC000 {
		t.Fatalf("the transparent pixel encoded as (%d, %d, %d) — dark, so it "+
			"was composited onto black rather than white", r>>8, g>>8, b>>8)
	}
}

// The finding that makes stripping metadata safe. A phone stores the sensor's
// landscape frame and a tag asking for a quarter turn; discarding the tag
// without turning the pixels puts every portrait photograph on its side.
func TestAnEXIFOrientationIsAppliedToThePixels(t *testing.T) {
	store := mediatest.NewMemory()
	// 6 is "rotate 90° clockwise", which is what a phone held upright writes.
	result, err := manager(t, store).Ingest(context.Background(),
		bytes.NewReader(mediatest.OrientedJPEG(t, 100, 50, 6)),
		request(media.Conversion{Name: "sm", Width: 400, Height: 400}))
	if err != nil {
		t.Fatalf("ingesting: %v", err)
	}

	if result.Source.Orientation != 6 {
		t.Fatalf("the orientation was read as %d, want 6", result.Source.Orientation)
	}
	// The stored frame is 100×50; displayed it is 50×100.
	if result.Source.Width != 50 || result.Source.Height != 100 {
		t.Fatalf("the source reads %d×%d, want 50×100 — the turned dimensions",
			result.Source.Width, result.Source.Height)
	}
	width, height, _ := dimensions(t, store.Bytes(t, result.Original.Path))
	if width != 50 || height != 100 {
		t.Fatalf("the stored original is %d×%d, so the pixels were not turned "+
			"and every portrait photograph is on its side", width, height)
	}
}

// And a 1 must not cost a copy or change anything.
func TestAnUnorientedJPEGIsLeftAlone(t *testing.T) {
	store := mediatest.NewMemory()
	result, err := manager(t, store).Ingest(context.Background(),
		bytes.NewReader(mediatest.OrientedJPEG(t, 100, 50, 1)),
		request())
	if err != nil {
		t.Fatalf("ingesting: %v", err)
	}
	if result.Source.Width != 100 || result.Source.Height != 50 {
		t.Fatalf("orientation 1 changed the dimensions to %d×%d",
			result.Source.Width, result.Source.Height)
	}
}

// The privacy half. A photograph carries GPS coordinates, a device serial
// number and a timestamp, and storing the submitted bytes publishes all three
// to everybody who can see the page.
func TestMetadataDoesNotSurviveIntoTheStoredOriginal(t *testing.T) {
	store := mediatest.NewMemory()
	result, err := manager(t, store).Ingest(context.Background(),
		bytes.NewReader(mediatest.OrientedJPEG(t, 80, 60, 1)),
		request())
	if err != nil {
		t.Fatalf("ingesting: %v", err)
	}
	if result.OriginalRaw {
		t.Fatal("the original was stored raw, which keeps its metadata")
	}
	if bytes.Contains(store.Bytes(t, result.Original.Path), []byte("Exif")) {
		t.Fatal("the stored original still carries an EXIF block")
	}
}

// RawOriginal is a decision, so it has to actually keep the bytes.
func TestRawOriginalKeepsTheSubmittedBytes(t *testing.T) {
	store := mediatest.NewMemory()
	raw := mediatest.OrientedJPEG(t, 80, 60, 1)

	req := request()
	req.Original = media.RawOriginal
	result, err := manager(t, store).Ingest(context.Background(), bytes.NewReader(raw), req)
	if err != nil {
		t.Fatalf("ingesting: %v", err)
	}
	if !result.OriginalRaw {
		t.Fatal("RawOriginal did not report itself")
	}
	if !bytes.Equal(store.Bytes(t, result.Original.Path), raw) {
		t.Fatal("RawOriginal re-encoded the image")
	}
}

func TestNoOriginalWritesOnlyTheConversions(t *testing.T) {
	store := mediatest.NewMemory()
	req := request(media.Conversion{Name: "sm", Width: 64, Height: 64})
	req.Original = media.NoOriginal

	result, err := manager(t, store).Ingest(context.Background(),
		bytes.NewReader(mediatest.OpaqueJPEG(t, 300, 300)), req)
	if err != nil {
		t.Fatalf("ingesting: %v", err)
	}
	if result.Original.Path != "" {
		t.Fatalf("an original was written to %q", result.Original.Path)
	}
	if store.Len() != 1 {
		t.Fatalf("the store holds %d files, want 1: %v", store.Len(), store.Paths())
	}
}

// The decode bomb, refused from the header. The file is small enough to pass
// any byte limit and asks a decoder for gigabytes; checking the size after
// decoding is checking it after the damage.
func TestADecodeBombIsRefusedFromItsHeader(t *testing.T) {
	store := mediatest.NewMemory()
	bomb := mediatest.HugeHeaderPNG(t, 50_000, 50_000)
	if len(bomb) > 4096 {
		t.Fatalf("the fixture is %d bytes, which is not the case under test", len(bomb))
	}

	started := time.Now()
	_, err := manager(t, store).Ingest(context.Background(), bytes.NewReader(bomb), request())
	elapsed := time.Since(started)

	if !errors.Is(err, media.ErrTooManyPixels) {
		t.Fatalf("a 50,000² image was not refused: %v", err)
	}
	// Ten gigabytes of RGBA cannot be allocated in a millisecond, so this
	// also proves the refusal happened before the decode rather than after.
	if elapsed > 500*time.Millisecond {
		t.Fatalf("the refusal took %v, so it was not taken from the header", elapsed)
	}
	if store.Len() != 0 {
		t.Fatalf("a refused upload left %v behind", store.Paths())
	}
	// The limit and the actual size both appear, because "too large" without
	// them sends the reader into the source.
	if !strings.Contains(err.Error(), "50000") {
		t.Fatalf("the refusal does not say how large the image was: %v", err)
	}
}

// The dimensions multiply past MaxInt32, so a guard that multiplies as int32
// overflows into a small positive number and admits the one image it exists
// to refuse.
func TestThePixelGuardDoesNotOverflow(t *testing.T) {
	store := mediatest.NewMemory()
	// 65536 × 65536 is exactly 2^32.
	bomb := mediatest.HugeHeaderPNG(t, 65_536, 65_536)
	if _, err := manager(t, store).Ingest(context.Background(),
		bytes.NewReader(bomb), request()); !errors.Is(err, media.ErrTooManyPixels) {
		t.Fatalf("a 65,536² image was not refused: %v", err)
	}
}

// Re-encoding an animated GIF writes its poster frame and discards the
// animation — silently, because the result is a valid image of the right
// size. So the submitted bytes are kept whatever the policy says.
func TestAnAnimatedGIFKeepsItsAnimation(t *testing.T) {
	store := mediatest.NewMemory()
	raw := mediatest.AnimatedGIF(t, 60, 40, 4)

	result, err := manager(t, store).Ingest(context.Background(), bytes.NewReader(raw),
		request(media.Conversion{Name: "sm", Width: 20, Height: 20}))
	if err != nil {
		t.Fatalf("ingesting: %v", err)
	}

	if result.Source.Frames != 4 {
		t.Fatalf("the frame count is %d, want 4", result.Source.Frames)
	}
	if !result.Source.Animated() {
		t.Fatal("a four-frame GIF does not report itself as animated")
	}
	if !result.OriginalRaw {
		t.Fatal("the animated original was re-encoded, which drops the animation")
	}

	animation, err := gif.DecodeAll(bytes.NewReader(store.Bytes(t, result.Original.Path)))
	if err != nil {
		t.Fatalf("the stored original is not a GIF: %v", err)
	}
	if len(animation.Image) != 4 {
		t.Fatalf("the stored original has %d frames, want 4", len(animation.Image))
	}

	// The variant is a still, which is the honest rendition: a thumbnail of
	// an animation is its first frame.
	variant, _ := result.Variant("sm")
	if variant.Format == "gif" {
		t.Fatal("the variant claims to be a GIF, so a caller would expect it to move")
	}
}

func TestASingleFrameGIFIsNotAnimated(t *testing.T) {
	store := mediatest.NewMemory()
	result, err := manager(t, store).Ingest(context.Background(),
		bytes.NewReader(mediatest.AnimatedGIF(t, 40, 40, 1)), request())
	if err != nil {
		t.Fatalf("ingesting: %v", err)
	}
	if result.Source.Frames != 1 || result.Source.Animated() {
		t.Fatalf("a one-frame GIF reports %d frames", result.Source.Frames)
	}
	if result.OriginalRaw {
		t.Fatal("a still GIF was stored raw, so its metadata survived for no reason")
	}
}

// A post whose row fails to insert has already had its images written. The
// same is true one level down: a conversion that fails after earlier ones
// were stored must leave nothing, because orphans are invisible until
// somebody measures the bucket.
func TestAFailedConversionLeavesNothingBehind(t *testing.T) {
	store := mediatest.NewMemory()
	store.FailOn = "_md"

	_, err := manager(t, store).Ingest(context.Background(),
		bytes.NewReader(mediatest.OpaqueJPEG(t, 400, 400)),
		request(
			media.Conversion{Name: "sm", Width: 100, Height: 100},
			media.Conversion{Name: "md", Width: 200, Height: 200},
		))
	if err == nil {
		t.Fatal("a failing store did not fail the ingest")
	}
	if store.Len() != 0 {
		t.Fatalf("the ingest left %v behind", store.Paths())
	}
}

func TestAnOversizeUploadIsRefused(t *testing.T) {
	store := mediatest.NewMemory()
	manager, err := media.New(media.Options{Store: store, MaxBytes: 1024})
	if err != nil {
		t.Fatalf("building the manager: %v", err)
	}
	_, err = manager.Ingest(context.Background(),
		bytes.NewReader(mediatest.OpaqueJPEG(t, 800, 800)), request())
	if !errors.Is(err, media.ErrTooManyBytes) {
		t.Fatalf("a 1KB limit accepted an 800×800 JPEG: %v", err)
	}
}

func TestSomethingThatIsNotAnImageIsRefused(t *testing.T) {
	store := mediatest.NewMemory()
	_, err := manager(t, store).Ingest(context.Background(),
		strings.NewReader("<html><script>alert(1)</script>"), request())
	if !errors.Is(err, media.ErrNotAnImage) {
		t.Fatalf("an HTML document was accepted as an image: %v", err)
	}
}

func TestABadRequestIsRefusedBeforeAnythingIsRead(t *testing.T) {
	cases := []struct {
		name string
		req  media.Request
		want error
	}{
		{"an empty dir", media.Request{Base: "x"}, media.ErrBadRequest},
		{"an empty base", media.Request{Dir: "posts"}, media.ErrBadRequest},
		{"a climbing dir", media.Request{Dir: "../etc", Base: "x"}, media.ErrBadRequest},
		{"an absolute dir", media.Request{Dir: "/etc", Base: "x"}, media.ErrBadRequest},
		{"a climbing base", media.Request{Dir: "posts", Base: "../x"}, media.ErrBadRequest},
		{
			"a conversion with a slash in its name",
			media.Request{Dir: "posts", Base: "x", Conversions: []media.Conversion{
				{Name: "a/b", Width: 10},
			}},
			media.ErrBadConversion,
		},
		{
			"a conversion with a dot in its name",
			media.Request{Dir: "posts", Base: "x", Conversions: []media.Conversion{
				{Name: "a.jpg", Width: 10},
			}},
			media.ErrBadConversion,
		},
		{
			"a conversion bounding nothing",
			media.Request{Dir: "posts", Base: "x", Conversions: []media.Conversion{
				{Name: "sm"},
			}},
			media.ErrBadConversion,
		},
		{
			"two conversions with one name",
			media.Request{Dir: "posts", Base: "x", Conversions: []media.Conversion{
				{Name: "sm", Width: 10}, {Name: "sm", Width: 20},
			}},
			media.ErrBadConversion,
		},
	}

	for _, each := range cases {
		t.Run(each.name, func(t *testing.T) {
			store := mediatest.NewMemory()
			_, err := manager(t, store).Ingest(context.Background(),
				bytes.NewReader(mediatest.OpaqueJPEG(t, 40, 40)), each.req)
			if !errors.Is(err, each.want) {
				t.Fatalf("got %v, want %v", err, each.want)
			}
			if store.Len() != 0 {
				t.Fatalf("a refused request wrote %v", store.Paths())
			}
		})
	}
}

// Remove's usual caller is undoing an ingest after a transaction rolled back,
// and whether a particular write landed is exactly what it cannot know.
func TestRemoveToleratesAPathThatWasNeverWritten(t *testing.T) {
	store := mediatest.NewMemory()
	m := manager(t, store)
	result, err := m.Ingest(context.Background(),
		bytes.NewReader(mediatest.OpaqueJPEG(t, 100, 100)),
		request(media.Conversion{Name: "sm", Width: 40, Height: 40}))
	if err != nil {
		t.Fatalf("ingesting: %v", err)
	}

	paths := append(result.Paths(), "posts/never-written.jpg", "")
	if err := m.Remove(paths...); err != nil {
		t.Fatalf("Remove reported a failure for a missing path: %v", err)
	}
	if store.Len() != 0 {
		t.Fatalf("Remove left %v behind", store.Paths())
	}
}

// Paths is what a caller hands back to Remove, so it has to be complete.
func TestPathsListsEverythingThatWasWritten(t *testing.T) {
	store := mediatest.NewMemory()
	result, err := manager(t, store).Ingest(context.Background(),
		bytes.NewReader(mediatest.OpaqueJPEG(t, 300, 300)),
		request(
			media.Conversion{Name: "sm", Width: 50, Height: 50},
			media.Conversion{Name: "md", Width: 150, Height: 150},
		))
	if err != nil {
		t.Fatalf("ingesting: %v", err)
	}
	if got := len(result.Paths()); got != store.Len() || got != 3 {
		t.Fatalf("Paths lists %d files and the store holds %d; want 3 of each",
			got, store.Len())
	}
	for _, path := range result.Paths() {
		if exists, _ := store.Exists(path); !exists {
			t.Fatalf("Paths names %q, which was not written", path)
		}
	}
}

// A cancelled request stops between conversions. It cannot stop inside a
// write, because fsys exposes no context on any method.
func TestACancelledContextStopsBetweenConversions(t *testing.T) {
	store := mediatest.NewMemory()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := manager(t, store).Ingest(ctx,
		bytes.NewReader(mediatest.OpaqueJPEG(t, 400, 400)),
		request(media.Conversion{Name: "sm", Width: 100, Height: 100}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want a cancellation", err)
	}
	if store.Len() != 0 {
		t.Fatalf("a cancelled ingest left %v behind", store.Paths())
	}
}

func TestVariantPathIsPredictableOnlyForAPinnedFormat(t *testing.T) {
	if path, ok := media.VariantPath("posts", "9-17600", "sm", media.FormatJPEG); !ok ||
		path != "posts/9-17600_sm.jpg" {
		t.Fatalf("got (%q, %v)", path, ok)
	}
	if path, ok := media.VariantPath("posts", "9-17600", "", media.FormatPNG); !ok ||
		path != "posts/9-17600.png" {
		t.Fatalf("the original's path is (%q, %v)", path, ok)
	}
	if _, ok := media.VariantPath("posts", "9-17600", "sm", media.FormatAuto); ok {
		t.Fatal("FormatAuto reported a predictable path, but the extension " +
			"depends on the source's pixels")
	}
}

func TestNewRefusesAManagerThatCannotWrite(t *testing.T) {
	if _, err := media.New(media.Options{}); err == nil {
		t.Fatal("a manager with no Store was accepted")
	}
	if _, err := media.New(media.Options{
		Store: mediatest.NewMemory(), Quality: 101,
	}); err == nil {
		t.Fatal("a JPEG quality of 101 was accepted")
	}
}

func TestTheMemoryStoreHonoursTheContract(t *testing.T) {
	mediatest.Contract(t, func(t *testing.T) media.Store {
		return mediatest.NewMemory()
	})
}
