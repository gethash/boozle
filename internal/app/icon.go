package app

import (
	"bytes"
	_ "embed"
	"image"
	"image/png"

	"github.com/hajimehoshi/ebiten/v2"
)

//go:embed assets/icon.png
var iconPNG []byte

// setWindowIcon applies the embedded boozle icon to the current window.
// macOS ignores SetWindowIcon and reads from the .app bundle's Info.plist
// instead — this is a no-op there but still correct on Linux and Windows.
func setWindowIcon() {
	img, err := png.Decode(bytes.NewReader(iconPNG))
	if err != nil {
		return
	}
	ebiten.SetWindowIcon([]image.Image{img})
}
