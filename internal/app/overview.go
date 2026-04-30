package app

import (
	"fmt"
	"image/color"
	"math"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

const (
	ovEnterFrames = 25
	ovExitFrames  = 18
)

type ovPhase int

const (
	ovOff     ovPhase = iota
	ovEntering         // anim 0 → 1
	ovActive
	ovExiting // anim 0 → 1
)

type thumbLoad struct {
	listIdx int
	img     *ebiten.Image
}

type overview struct {
	phase    ovPhase
	anim     float64
	cols     int
	rows     int
	cellW    float64
	cellH    float64
	padding  float64
	rightX   int            // physical-pixel x where the right-half grid begins
	thumbs   []*ebiten.Image // indexed by listIdx; nil = not yet loaded
	thumbCh  chan thumbLoad
	thumbStop chan struct{}
	fromIdx   int // listIdx when overview was opened
	selIdx    int // keyboard-selected cell (list index)
	exitToIdx int // destination when closing
	hoverIdx  int // -1 = none
	initMx    int // cursor x when overview opened — hover is suppressed until cursor moves
	initMy    int
}

// computeOvGrid lays out the thumbnail grid on the right half of the screen.
func computeOvGrid(n, bufW, bufH int) (rightX, cols, rows int, cellW, cellH, padding float64) {
	rightX = bufW / 2
	rightW := bufW - rightX
	padding = 12.0
	if n == 0 {
		return rightX, 1, 1, float64(rightW) - 2*padding, float64(bufH) - 2*padding, padding
	}
	aspect := float64(rightW) / float64(bufH)
	cols = max(1, int(math.Ceil(math.Sqrt(float64(n)*aspect))))
	rows = (n + cols - 1) / cols
	cellW = (float64(rightW) - padding*float64(cols+1)) / float64(cols)
	cellH = (float64(bufH) - padding*float64(rows+1)) / float64(rows)
	return
}

func (ov *overview) cellRect(i int) (x, y, w, h float64) {
	col := i % ov.cols
	row := i / ov.cols
	x = float64(ov.rightX) + ov.padding + float64(col)*(ov.cellW+ov.padding)
	y = ov.padding + float64(row)*(ov.cellH+ov.padding)
	return x, y, ov.cellW, ov.cellH
}

func easeInOut(t float64) float64 {
	t = clamp01(t)
	if t < 0.5 {
		return 4 * t * t * t
	}
	return 1 - math.Pow(-2*t+2, 3)/2
}

func easeOut(t float64) float64 {
	t = clamp01(t)
	return 1 - math.Pow(1-t, 3)
}

func clamp01(t float64) float64 {
	if t < 0 {
		return 0
	}
	if t > 1 {
		return 1
	}
	return t
}

func lerpF(a, b, t float64) float64 {
	return a + (b-a)*t
}

// openOverview initialises overview mode and kicks off thumbnail loading.
func (g *Game) openOverview() {
	n := len(g.pageList)
	rightX, cols, rows, cellW, cellH, padding := computeOvGrid(n, g.bufW, g.bufH)
	g.ov = overview{
		phase:     ovEntering,
		cols:      cols,
		rows:      rows,
		cellW:     cellW,
		cellH:     cellH,
		padding:   padding,
		rightX:    rightX,
		thumbs:    make([]*ebiten.Image, n),
		thumbCh:   make(chan thumbLoad, n),
		thumbStop: make(chan struct{}),
		fromIdx:   g.listIdx,
		selIdx:    g.listIdx,
		exitToIdx: g.listIdx,
		hoverIdx:  -1,
	}
	g.ov.initMx, g.ov.initMy = ebiten.CursorPosition()
	g.startThumbLoader()
}

// startThumbLoader renders every slide at thumbnail size on a background
// goroutine and sends the results on thumbCh. Captures all values by value so
// zeroing g.ov later is safe.
func (g *Game) startThumbLoader() {
	thumbStop := g.ov.thumbStop
	thumbCh := g.ov.thumbCh
	pageList := g.pageList
	doc := g.doc
	w := max(1, int(math.Round(g.ov.cellW)))
	h := max(1, int(math.Round(g.ov.cellH)))
	go func() {
		for i, pageIdx := range pageList {
			select {
			case <-thumbStop:
				return
			default:
			}
			rgba, cleanup, err := doc.RenderPage(pageIdx, w, h)
			if err != nil {
				continue
			}
			eimg := ebiten.NewImageFromImage(rgba)
			cleanup()
			select {
			case thumbCh <- thumbLoad{i, eimg}:
			case <-thumbStop:
				eimg.Deallocate()
				return
			}
		}
	}()
}

// closeOverview signals the thumbnail loader to stop and begins the exit animation.
func (g *Game) closeOverview(targetIdx int) {
	close(g.ov.thumbStop)
	g.ov.exitToIdx = targetIdx
	g.ov.phase = ovExiting
	g.ov.anim = 0
}

// updateOverview drives animation, handles input, and finalises the transition.
// It is called from Update() whenever ov.phase != ovOff.
func (g *Game) updateOverview() error {
	ov := &g.ov

	// Drain incoming thumbnails (up to 10 per frame to avoid stalls).
thumbDrain:
	for range 10 {
		select {
		case tl := <-ov.thumbCh:
			if tl.listIdx >= 0 && tl.listIdx < len(ov.thumbs) {
				ov.thumbs[tl.listIdx] = tl.img
			}
		default:
			break thumbDrain
		}
	}

	switch ov.phase {
	case ovEntering:
		ov.anim += 1.0 / ovEnterFrames
		if ov.anim >= 1 {
			ov.anim = 1
			ov.phase = ovActive
		}

	case ovActive:
		n := len(g.pageList)
		if inpututil.IsKeyJustPressed(ebiten.KeyEscape) || inpututil.IsKeyJustPressed(ebiten.KeyTab) {
			g.closeOverview(ov.fromIdx)
			return nil
		}
		if inpututil.IsKeyJustPressed(ebiten.KeyQ) {
			return ErrQuit
		}
		if inpututil.IsKeyJustPressed(ebiten.KeyArrowRight) {
			ov.selIdx = (ov.selIdx + 1) % n
		}
		if inpututil.IsKeyJustPressed(ebiten.KeyArrowLeft) {
			ov.selIdx = (ov.selIdx - 1 + n) % n
		}
		if inpututil.IsKeyJustPressed(ebiten.KeyArrowDown) {
			if next := ov.selIdx + ov.cols; next < n {
				ov.selIdx = next
			}
		}
		if inpututil.IsKeyJustPressed(ebiten.KeyArrowUp) {
			if prev := ov.selIdx - ov.cols; prev >= 0 {
				ov.selIdx = prev
			}
		}
		if inpututil.IsKeyJustPressed(ebiten.KeyEnter) || inpututil.IsKeyJustPressed(ebiten.KeyNumpadEnter) {
			g.closeOverview(ov.selIdx)
			return nil
		}
		// Mouse hover hit-test — suppressed until the cursor actually moves after
		// the overview opens, so tiles aren't highlighted by the resting cursor.
		mx, my := ebiten.CursorPosition()
		ov.hoverIdx = -1
		if mx != ov.initMx || my != ov.initMy {
			for i := range n {
				x, y, w, h := ov.cellRect(i)
				if float64(mx) >= x && float64(mx) < x+w && float64(my) >= y && float64(my) < y+h {
					ov.hoverIdx = i
					break
				}
			}
		}
		if inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft) && ov.hoverIdx >= 0 {
			g.closeOverview(ov.hoverIdx)
			return nil
		}

	case ovExiting:
		ov.anim += 1.0 / ovExitFrames
		if ov.anim >= 1 {
			exitTo := ov.exitToIdx
			from := ov.fromIdx
			// Dispose thumbnails.
			for _, img := range ov.thumbs {
				if img != nil {
					img.Deallocate()
				}
			}
			// Drain any in-flight results.
		finalDrain:
			for {
				select {
				case tl := <-ov.thumbCh:
					if tl.img != nil {
						tl.img.Deallocate()
					}
				default:
					break finalDrain
				}
			}
			// Reset overview state, then wire up navigation.
			g.ov = overview{}
			g.listIdx = exitTo
			g.prevListIdx = from
			g.auto.Reset(g.pageList[exitTo] + 1)
			if g.bufW > 0 && g.bufH > 0 {
				if err := g.maybeRefreshDisplay(); err != nil {
					return err
				}
				g.prefetchNeighbors()
			}
		}
	}
	return nil
}

// drawOverview renders the split-view overview: large preview on the left,
// thumbnail grid on the right.
func (g *Game) drawOverview(screen *ebiten.Image) {
	ov := &g.ov
	n := len(g.pageList)
	if n == 0 {
		return
	}
	W := float64(g.bufW)
	H := float64(g.bufH)

	// Dark scrim covering the full buffer.
	var scrimAlpha float64
	switch ov.phase {
	case ovEntering:
		scrimAlpha = lerpF(0, 200, easeInOut(ov.anim))
	case ovActive:
		scrimAlpha = 200
	case ovExiting:
		scrimAlpha = lerpF(200, 0, easeInOut(ov.anim))
	}
	vector.FillRect(screen, 0, 0, float32(W), float32(H),
		color.RGBA{8, 10, 18, uint8(scrimAlpha)}, false)

	// Large preview on the left half.
	g.drawOverviewPreview(screen)

	// Subtle divider between the two halves (active phase only).
	if ov.phase == ovActive {
		vector.FillRect(screen,
			float32(ov.rightX)-1, 0, 1, float32(H),
			color.RGBA{255, 255, 255, 18}, false)
	}

	// Thumbnail grid on the right half.
	for i := range n {
		g.drawOverviewTile(screen, i)
	}

	// Slide label centered in the left half (active phase only).
	if ov.phase == ovActive && ov.selIdx >= 0 && ov.selIdx < n {
		page1 := g.pageList[ov.selIdx] + 1
		label := fmt.Sprintf("p. %d / %d", page1, n)
		labelX := int(float64(ov.rightX)/2) - len(label)*3
		ebitenutil.DebugPrintAt(screen, label, labelX, g.bufH-20)
	}
}

// drawOverviewPreview draws the large current/selected slide on the left half,
// animating in from the slide's normal display position on enter, and zooming
// to fill the screen on exit.
func (g *Game) drawOverviewPreview(screen *ebiten.Image) {
	ov := &g.ov
	leftW := float64(ov.rightX)
	leftH := float64(g.bufH)

	// Select the image to display.
	var img *ebiten.Image
	switch ov.phase {
	case ovEntering:
		img = g.display
		if img == nil {
			img = ov.thumbs[ov.fromIdx]
		}
	case ovActive:
		if ov.selIdx == ov.fromIdx {
			img = g.display
		} else {
			img = ov.thumbs[ov.selIdx]
		}
		if img == nil {
			img = g.display
		}
	case ovExiting:
		if ov.exitToIdx == ov.fromIdx {
			img = g.display
		} else {
			img = ov.thumbs[ov.exitToIdx]
		}
		if img == nil {
			img = g.display
		}
	}

	if img == nil {
		return
	}
	iW := float64(img.Bounds().Dx())
	iH := float64(img.Bounds().Dy())
	if iW <= 0 || iH <= 0 {
		return
	}

	// Aspect-fit target inside the left half.
	s := math.Min(leftW/iW, leftH/iH)
	fitW := iW * s
	fitH := iH * s
	fitX := (leftW - fitW) / 2
	fitY := (leftH - fitH) / 2

	var tx, ty, tw, th float64

	switch ov.phase {
	case ovEntering:
		localT := easeInOut(clamp01(ov.anim / 0.7))
		tx = lerpF(float64(g.displayBounds.dstX), fitX, localT)
		ty = lerpF(float64(g.displayBounds.dstY), fitY, localT)
		tw = lerpF(float64(g.displayBounds.dstW), fitW, localT)
		th = lerpF(float64(g.displayBounds.dstH), fitH, localT)

	case ovActive:
		tx, ty, tw, th = fitX, fitY, fitW, fitH

	case ovExiting:
		// Zoom to the aspect-fit position within the full screen.
		sf := math.Min(float64(g.bufW)/iW, float64(g.bufH)/iH)
		fullW := math.Round(iW * sf)
		fullH := math.Round(iH * sf)
		fullX := (float64(g.bufW) - fullW) / 2
		fullY := (float64(g.bufH) - fullH) / 2
		localT := easeInOut(ov.anim)
		tx = lerpF(fitX, fullX, localT)
		ty = lerpF(fitY, fullY, localT)
		tw = lerpF(fitW, fullW, localT)
		th = lerpF(fitH, fullH, localT)
	}

	if tw < 1 || th < 1 {
		return
	}

	op := &ebiten.DrawImageOptions{}
	op.GeoM.Scale(tw/iW, th/iH)
	op.GeoM.Translate(tx, ty)
	screen.DrawImage(img, op)
}

// drawOverviewTile draws one thumbnail in the right-half grid.
func (g *Game) drawOverviewTile(screen *ebiten.Image, i int) {
	ov := &g.ov
	n := len(g.pageList)

	cx, cy, cw, ch := ov.cellRect(i)
	var tx, ty, tw, th float64
	var alpha float64 = 1.0

	switch ov.phase {
	case ovEntering:
		// All tiles cascade in from their cell centres.
		dist := i - ov.fromIdx
		if dist < 0 {
			dist = -dist
		}
		maxDist := ov.fromIdx
		if tail := n - 1 - ov.fromIdx; tail > maxDist {
			maxDist = tail
		}
		if maxDist == 0 {
			maxDist = 1
		}
		nd := float64(dist) / float64(maxDist)
		startT := 0.25 + nd*0.40
		localT := easeOut(clamp01((ov.anim - startT) / 0.35))
		tx = lerpF(cx+cw*0.5, cx, localT)
		ty = lerpF(cy+ch*0.5, cy, localT)
		tw = lerpF(0, cw, localT)
		th = lerpF(0, ch, localT)
		alpha = clamp01(localT * 1.5)

	case ovActive:
		tx, ty, tw, th = cx, cy, cw, ch

	case ovExiting:
		// All grid tiles fade out while the left preview zooms to fill the screen.
		tx, ty, tw, th = cx, cy, cw, ch
		alpha = lerpF(1, 0, clamp01(ov.anim*2))
	}

	if tw < 1 || th < 1 || alpha <= 0 {
		return
	}

	img := ov.thumbs[i]
	if img != nil {
		iW := float64(img.Bounds().Dx())
		iH := float64(img.Bounds().Dy())
		if iW > 0 && iH > 0 {
			// Aspect-fit within the cell.
			sc := math.Min(tw/iW, th/iH)
			dw := iW * sc
			dh := iH * sc
			op := &ebiten.DrawImageOptions{}
			op.GeoM.Scale(sc, sc)
			op.GeoM.Translate(tx+(tw-dw)/2, ty+(th-dh)/2)
			op.ColorScale.ScaleAlpha(float32(alpha))
			screen.DrawImage(img, op)
		}
	} else {
		// Placeholder rectangle.
		if a := uint8(alpha * 200); a > 0 {
			vector.FillRect(screen,
				float32(tx), float32(ty), float32(tw), float32(th),
				color.RGBA{31, 41, 55, a}, false)
		}
	}

	// Selection / hover border (active phase only).
	if ov.phase != ovActive {
		return
	}
	switch {
	case i == ov.selIdx && i != ov.fromIdx:
		gradT := float32(0)
		if n > 1 {
			gradT = float32(i) / float32(n-1)
		}
		c := rainbowAt(gradT, 255)
		bw := float64(2)
		vector.FillRect(screen, float32(tx), float32(ty), float32(tw), float32(bw), c, false)
		vector.FillRect(screen, float32(tx), float32(ty+th-bw), float32(tw), float32(bw), c, false)
		vector.FillRect(screen, float32(tx), float32(ty), float32(bw), float32(th), c, false)
		vector.FillRect(screen, float32(tx+tw-bw), float32(ty), float32(bw), float32(th), c, false)
	case i == ov.fromIdx:
		// Dim grey border — "you are here" indicator.
		c := color.RGBA{180, 180, 180, 160}
		bw := float64(2)
		vector.FillRect(screen, float32(tx), float32(ty), float32(tw), float32(bw), c, false)
		vector.FillRect(screen, float32(tx), float32(ty+th-bw), float32(tw), float32(bw), c, false)
		vector.FillRect(screen, float32(tx), float32(ty), float32(bw), float32(th), c, false)
		vector.FillRect(screen, float32(tx+tw-bw), float32(ty), float32(bw), float32(th), c, false)
	case i == ov.hoverIdx:
		c := color.RGBA{255, 255, 255, 120}
		bw := float64(1)
		vector.FillRect(screen, float32(tx), float32(ty), float32(tw), float32(bw), c, false)
		vector.FillRect(screen, float32(tx), float32(ty+th-bw), float32(tw), float32(bw), c, false)
		vector.FillRect(screen, float32(tx), float32(ty), float32(bw), float32(th), c, false)
		vector.FillRect(screen, float32(tx+tw-bw), float32(ty), float32(bw), float32(th), c, false)
	}
}
