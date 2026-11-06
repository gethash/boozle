package app

import (
	"bytes"
	_ "embed"
	"fmt"
	"math"
	"sync"

	"github.com/hajimehoshi/ebiten/v2/text/v2"
)

// The design system's text faces: Geist for everything you read, Geist Mono
// for labels, counters and timers. Both are the upstream variable fonts
// (SIL OFL 1.1, see fonts/OFL.txt and NOTICE); weights are set through the
// wght axis.
var (
	//go:embed fonts/Geist-Variable.ttf
	geistTTF []byte
	//go:embed fonts/GeistMono-Variable.ttf
	geistMonoTTF []byte
)

type fontFamily int

const (
	fontSans fontFamily = iota // Geist
	fontMono                   // Geist Mono
)

var fontSources struct {
	once       sync.Once
	sans, mono *text.GoTextFaceSource
}

func fontSource(f fontFamily) *text.GoTextFaceSource {
	fontSources.once.Do(func() {
		parse := func(name string, b []byte) *text.GoTextFaceSource {
			src, err := text.NewGoTextFaceSource(bytes.NewReader(b))
			if err != nil {
				panic(fmt.Sprintf("boozle: embedded font %s: %v", name, err))
			}
			return src
		}
		fontSources.sans = parse("Geist", geistTTF)
		fontSources.mono = parse("Geist Mono", geistMonoTTF)
	})
	if f == fontMono {
		return fontSources.mono
	}
	return fontSources.sans
}

type faceKey struct {
	family fontFamily
	size4  int // size in quarter pixels
	weight int
}

// maxCachedFaces bounds the face cache; sizes follow the window size, so a
// long session of resizes would otherwise keep adding entries.
const maxCachedFaces = 256

var faces = struct {
	sync.Mutex
	m map[faceKey]*text.GoTextFace
}{m: map[faceKey]*text.GoTextFace{}}

var tagWght = text.MustParseTag("wght")

// uiFace returns a cached face of family at size buffer pixels (rounded to
// a quarter pixel) and the given wght axis value.
func uiFace(family fontFamily, size float64, weight int) *text.GoTextFace {
	size = max(1, math.Round(size*4)/4)
	k := faceKey{family: family, size4: int(size * 4), weight: weight}
	faces.Lock()
	defer faces.Unlock()
	if f, ok := faces.m[k]; ok {
		return f
	}
	if len(faces.m) >= maxCachedFaces {
		clear(faces.m)
	}
	f := &text.GoTextFace{Source: fontSource(family), Size: size}
	f.SetVariation(tagWght, float32(weight))
	faces.m[k] = f
	return f
}

// textStyle is a design-system type style. size is in design pixels and
// tracking in em; face and trackingPx scale them by the window's UI unit.
type textStyle struct {
	family   fontFamily
	size     float64
	weight   int
	tracking float64
}

var (
	// eyebrow: uppercase pane and card labels (CURRENT, CLOCK …).
	styleEyebrow = textStyle{family: fontMono, size: 12, weight: 500, tracking: 0.08}
	// body-s: speaker notes. Notes are fitted, so size is the minimum.
	styleNotes = textStyle{family: fontSans, size: 15, weight: 400}
	// timer: clock, counters, elapsed and remaining time. Sized to fit.
	styleTimer = textStyle{family: fontMono, size: 56, weight: 500, tracking: -0.03}
	// counter: the HUD's slide counter and countdown.
	styleCounter = textStyle{family: fontMono, size: 13, weight: 500}
)

// face returns the style's face at u buffer pixels per design pixel.
func (s textStyle) face(u float64) *text.GoTextFace {
	return uiFace(s.family, s.size*u, s.weight)
}

// faceAt returns the style's face at an explicit size in buffer pixels.
func (s textStyle) faceAt(px float64) *text.GoTextFace {
	return uiFace(s.family, px, s.weight)
}

// trackingPx is the style's letter spacing for face f, in buffer pixels.
func (s textStyle) trackingPx(f *text.GoTextFace) float64 {
	return s.tracking * f.Size
}
