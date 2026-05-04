package app

import (
	"fmt"
	"image/color"
	"math"
	"sort"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/vector"

	"github.com/gethash/boozle/internal/ipc"
)

const (
	ovEnterFrames = 25
	ovExitFrames  = 18

	overviewMaxThumbPixels       = 180
	overviewThumbQueue           = 64
	overviewThumbsPerSelection   = 32
	overviewBackgroundPerFrame   = 4
	overviewThumbNeighborhoodRow = 2
)

type ovPhase int

const (
	ovOff      ovPhase = iota
	ovEntering         // anim 0 → 1
	ovActive
	ovExiting // anim 0 → 1
)

type thumbLoad struct {
	listIdx int
	img     *ebiten.Image
}

type overview struct {
	phase     ovPhase
	anim      float64
	cols      int
	cellW     float64
	cellH     float64
	padding   float64
	gridX     int
	gridY     int
	thumbs    []*ebiten.Image // indexed by listIdx; nil = not yet loaded
	thumbCh   chan thumbLoad
	thumbReq  chan int
	thumbStop chan struct{}
	requested []bool
	nextThumb int
	fromIdx   int // listIdx when overview was opened
	selIdx    int // keyboard-selected cell (list index)
	exitToIdx int // destination when closing
	hoverIdx  int // -1 = none
	initMx    int // cursor x when overview opened — hover is suppressed until cursor moves
	initMy    int
}

type overviewLayout struct {
	previewPanel   presenterRect
	previewContent presenterRect
	gridPanel      presenterRect
	gridContent    presenterRect
}

func computeOverviewLayout(bufW, bufH int) overviewLayout {
	margin := max(18, min(bufW, bufH)/52)
	gap := max(14, margin/2)
	rightW := int(float64(bufW) * 0.42)
	rightW = max(rightW, 380)
	rightW = min(rightW, int(float64(bufW)*0.55))
	leftW := bufW - margin*2 - gap - rightW
	if leftW < 1 {
		leftW = 1
	}
	fullH := bufH - margin*2
	if fullH < 1 {
		fullH = 1
	}

	previewPanel := presenterRect{x: margin, y: margin, w: leftW, h: fullH}
	gridPanel := presenterRect{x: margin + leftW + gap, y: margin, w: rightW, h: fullH}
	return overviewLayout{
		previewPanel:   previewPanel,
		previewContent: insetPresenterRect(previewPanel, 16, 44, 16, 44),
		gridPanel:      gridPanel,
		gridContent:    insetPresenterRect(gridPanel, 14, 44, 14, 14),
	}
}

// computeOvGrid lays out the thumbnail grid inside the framed overview panel.
func computeOvGrid(n, bufW, bufH int) (gridX, gridY, cols, rows int, cellW, cellH, padding float64) {
	content := computeOverviewLayout(bufW, bufH).gridContent
	padding = 12.0
	if n == 0 {
		return content.x, content.y, 1, 1, float64(content.w) - 2*padding, float64(content.h) - 2*padding, padding
	}
	aspect := float64(content.w) / float64(content.h)
	cols = max(1, int(math.Ceil(math.Sqrt(float64(n)*aspect))))
	rows = (n + cols - 1) / cols
	cellW = (float64(content.w) - padding*float64(cols+1)) / float64(cols)
	cellH = (float64(content.h) - padding*float64(rows+1)) / float64(rows)
	gridX = content.x
	gridY = content.y
	return
}

func (ov *overview) cellRect(i int) (x, y, w, h float64) {
	col := i % ov.cols
	row := i / ov.cols
	x = float64(ov.gridX) + ov.padding + float64(col)*(ov.cellW+ov.padding)
	y = float64(ov.gridY) + ov.padding + float64(row)*(ov.cellH+ov.padding)
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

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// openOverview initialises overview mode and kicks off thumbnail loading.
func (g *Game) openOverview() {
	n := len(g.pageList)
	gridX, gridY, cols, _, cellW, cellH, padding := computeOvGrid(n, g.bufW, g.bufH)
	g.ov = overview{
		phase:     ovEntering,
		cols:      cols,
		cellW:     cellW,
		cellH:     cellH,
		padding:   padding,
		gridX:     gridX,
		gridY:     gridY,
		thumbs:    make([]*ebiten.Image, n),
		thumbCh:   make(chan thumbLoad, overviewThumbQueue),
		thumbReq:  make(chan int, overviewThumbQueue),
		thumbStop: make(chan struct{}),
		requested: make([]bool, n),
		nextThumb: 0,
		fromIdx:   g.listIdx,
		selIdx:    g.listIdx,
		exitToIdx: g.listIdx,
		hoverIdx:  -1,
	}
	g.ov.initMx, g.ov.initMy = ebiten.CursorPosition()
	g.startThumbLoader()
	g.requestOverviewThumbnails(g.listIdx)
}

// startThumbLoader renders requested slides at capped thumbnail size on a
// background goroutine. Captures all values by value so zeroing g.ov later is safe.
func (g *Game) startThumbLoader() {
	thumbStop := g.ov.thumbStop
	thumbCh := g.ov.thumbCh
	thumbReq := g.ov.thumbReq
	pageList := g.pageList
	doc := g.doc
	w := max(1, min(overviewMaxThumbPixels, int(math.Round(g.ov.cellW))))
	h := max(1, min(overviewMaxThumbPixels, int(math.Round(g.ov.cellH))))
	go func() {
		for {
			select {
			case <-thumbStop:
				return
			case i := <-thumbReq:
				if i < 0 || i >= len(pageList) {
					continue
				}
				pageIdx := pageList[i]
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
		}
	}()
}

func (g *Game) requestOverviewThumbnails(center int) {
	ov := &g.ov
	for _, idx := range overviewLoadOrder(len(g.pageList), center, ov.fromIdx, ov.cols) {
		ok, stopped := g.requestOverviewThumbnail(idx)
		if stopped {
			return
		}
		if !ok {
			return
		}
	}
}

func (g *Game) requestMoreOverviewThumbnails(limit int) {
	ov := &g.ov
	if limit <= 0 || len(g.pageList) == 0 {
		return
	}
	queued := 0
	scanned := 0
	for queued < limit && scanned < len(g.pageList) {
		idx := ov.nextThumb
		ov.nextThumb = (ov.nextThumb + 1) % len(g.pageList)
		scanned++
		ok, stopped := g.requestOverviewThumbnail(idx)
		if stopped || !ok {
			return
		}
		queued++
	}
}

func (g *Game) requestOverviewThumbnail(idx int) (queued bool, stopped bool) {
	ov := &g.ov
	if idx < 0 || idx >= len(ov.requested) || ov.requested[idx] {
		return true, false
	}
	if idx < len(ov.thumbs) && ov.thumbs[idx] != nil {
		return true, false
	}
	select {
	case <-ov.thumbStop:
		return false, true
	case ov.thumbReq <- idx:
		ov.requested[idx] = true
		return true, false
	default:
		return false, false
	}
}

func overviewLoadOrder(n, center, include, cols int) []int {
	if n <= 0 {
		return nil
	}
	center = max(0, min(n-1, center))
	include = max(0, min(n-1, include))
	if cols < 1 {
		cols = 1
	}
	seen := make(map[int]struct{}, overviewThumbsPerSelection)
	add := func(out []int, idx int) []int {
		if idx < 0 || idx >= n {
			return out
		}
		if _, ok := seen[idx]; ok {
			return out
		}
		seen[idx] = struct{}{}
		return append(out, idx)
	}

	out := make([]int, 0, overviewThumbsPerSelection)
	out = add(out, center)
	out = add(out, include)

	type candidate struct {
		idx  int
		dist int
	}
	candidates := make([]candidate, 0, overviewThumbsPerSelection*2)
	centerRow, centerCol := center/cols, center%cols
	for row := centerRow - overviewThumbNeighborhoodRow; row <= centerRow+overviewThumbNeighborhoodRow; row++ {
		if row < 0 {
			continue
		}
		for col := centerCol - overviewThumbNeighborhoodRow; col <= centerCol+overviewThumbNeighborhoodRow; col++ {
			if col < 0 || col >= cols {
				continue
			}
			idx := row*cols + col
			if idx >= n {
				continue
			}
			dist := abs(row-centerRow) + abs(col-centerCol)
			candidates = append(candidates, candidate{idx: idx, dist: dist})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].dist == candidates[j].dist {
			return candidates[i].idx < candidates[j].idx
		}
		return candidates[i].dist < candidates[j].dist
	})
	for _, cand := range candidates {
		out = add(out, cand.idx)
		if len(out) >= overviewThumbsPerSelection {
			return out
		}
	}
	for delta := 1; len(out) < overviewThumbsPerSelection && (center-delta >= 0 || center+delta < n); delta++ {
		out = add(out, center-delta)
		if len(out) >= overviewThumbsPerSelection {
			break
		}
		out = add(out, center+delta)
	}
	return out
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
func (g *Game) updateOverview(presenterCmds []ipc.PresenterCommand) error {
	ov := &g.ov

	// Drain incoming thumbnails (up to 10 per frame to avoid stalls).
thumbDrain:
	for range 10 {
		select {
		case tl := <-ov.thumbCh:
			if tl.listIdx >= 0 && tl.listIdx < len(ov.thumbs) {
				if ov.thumbs[tl.listIdx] != nil {
					ov.thumbs[tl.listIdx].Deallocate()
				}
				ov.thumbs[tl.listIdx] = tl.img
			}
		default:
			break thumbDrain
		}
	}
	if ov.phase == ovEntering || ov.phase == ovActive {
		g.requestMoreOverviewThumbnails(overviewBackgroundPerFrame)
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
		prevSel := ov.selIdx
		if inpututil.IsKeyJustPressed(ebiten.KeyEscape) ||
			inpututil.IsKeyJustPressed(ebiten.KeyTab) ||
			presenterCmdPressed(presenterCmds, presenterCmdEscape) ||
			presenterCmdPressed(presenterCmds, presenterCmdTab) {
			g.closeOverview(ov.fromIdx)
			return nil
		}
		if inpututil.IsKeyJustPressed(ebiten.KeyQ) ||
			presenterCmdPressed(presenterCmds, presenterCmdQuit) {
			return ErrQuit
		}
		if inpututil.IsKeyJustPressed(ebiten.KeyArrowRight) ||
			presenterCmdPressed(presenterCmds, presenterCmdRight) {
			ov.selIdx = (ov.selIdx + 1) % n
		}
		if inpututil.IsKeyJustPressed(ebiten.KeyArrowLeft) ||
			presenterCmdPressed(presenterCmds, presenterCmdLeft) {
			ov.selIdx = (ov.selIdx - 1 + n) % n
		}
		if inpututil.IsKeyJustPressed(ebiten.KeyArrowDown) ||
			presenterCmdPressed(presenterCmds, presenterCmdDown) {
			if next := ov.selIdx + ov.cols; next < n {
				ov.selIdx = next
			}
		}
		if inpututil.IsKeyJustPressed(ebiten.KeyArrowUp) ||
			presenterCmdPressed(presenterCmds, presenterCmdUp) {
			if prev := ov.selIdx - ov.cols; prev >= 0 {
				ov.selIdx = prev
			}
		}
		if inpututil.IsKeyJustPressed(ebiten.KeyEnter) ||
			inpututil.IsKeyJustPressed(ebiten.KeyNumpadEnter) ||
			presenterCmdPressed(presenterCmds, presenterCmdEnter) {
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
		if ov.selIdx != prevSel {
			g.requestOverviewThumbnails(ov.selIdx)
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
		scrimAlpha = lerpF(0, 230, easeInOut(ov.anim))
	case ovActive:
		scrimAlpha = 230
	case ovExiting:
		scrimAlpha = lerpF(230, 0, easeInOut(ov.anim))
	}

	vector.FillRect(screen, 0, 0, float32(W), float32(H),
		color.RGBA{8, 10, 18, uint8(scrimAlpha)}, false)

	lo := computeOverviewLayout(g.bufW, g.bufH)
	drawPresenterPanel(screen, lo.previewPanel)
	drawPresenterPanel(screen, lo.gridPanel)
	drawPresenterText(screen, "SELECTED", lo.previewPanel.x+18, lo.previewPanel.y+14, 2, color.RGBA{148, 163, 184, 255})
	drawPresenterText(screen, "OVERVIEW", lo.gridPanel.x+18, lo.gridPanel.y+14, 2, color.RGBA{148, 163, 184, 255})

	g.drawOverviewPreview(screen)

	for i := range n {
		g.drawOverviewTile(screen, i)
	}

	if ov.phase == ovActive && ov.selIdx >= 0 && ov.selIdx < n {
		page1 := g.pageList[ov.selIdx] + 1
		label := fmt.Sprintf("p. %d / %d", page1, n)
		drawPresenterText(screen, label, lo.previewPanel.x+18, lo.previewPanel.y+lo.previewPanel.h-32, 2, color.RGBA{248, 250, 252, 220})
	}
}

// drawOverviewPreview draws the large current/selected slide on the left half,
// animating in from the slide's normal display position on enter, and zooming
// to fill the screen on exit.
func (g *Game) drawOverviewPreview(screen *ebiten.Image) {
	ov := &g.ov
	content := computeOverviewLayout(g.bufW, g.bufH).previewContent
	leftW := float64(content.w)
	leftH := float64(content.h)

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
	fitX := float64(content.x) + (leftW-fitW)/2
	fitY := float64(content.y) + (leftH-fitH)/2

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

	// imgX/imgY/imgW/imgH track the actual rendered image bounds (aspect-fitted
	// within the cell). Borders are drawn at these bounds, not the cell bounds.
	var imgX, imgY, imgW, imgH float64

	img := ov.thumbs[i]
	if img != nil {
		iW := float64(img.Bounds().Dx())
		iH := float64(img.Bounds().Dy())
		if iW > 0 && iH > 0 {
			sc := math.Min(tw/iW, th/iH)
			imgW = iW * sc
			imgH = iH * sc
			imgX = tx + (tw-imgW)/2
			imgY = ty + (th-imgH)/2
			op := &ebiten.DrawImageOptions{}
			op.GeoM.Scale(sc, sc)
			op.GeoM.Translate(imgX, imgY)
			op.ColorScale.ScaleAlpha(float32(alpha))
			screen.DrawImage(img, op)
		}
	} else {
		// Placeholder: use cell bounds until the thumbnail loads.
		imgX, imgY, imgW, imgH = tx, ty, tw, th
		if a := uint8(alpha * 200); a > 0 {
			vector.FillRect(screen,
				float32(tx), float32(ty), float32(tw), float32(th),
				color.RGBA{31, 41, 55, a}, false)
		}
	}

	// Selection / hover border — flush against the actual slide image.
	if ov.phase != ovActive || imgW < 1 || imgH < 1 {
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
		vector.FillRect(screen, float32(imgX), float32(imgY), float32(imgW), float32(bw), c, false)
		vector.FillRect(screen, float32(imgX), float32(imgY+imgH-bw), float32(imgW), float32(bw), c, false)
		vector.FillRect(screen, float32(imgX), float32(imgY), float32(bw), float32(imgH), c, false)
		vector.FillRect(screen, float32(imgX+imgW-bw), float32(imgY), float32(bw), float32(imgH), c, false)
	case i == ov.fromIdx:
		// Dim grey border — "you are here" indicator.
		c := color.RGBA{180, 180, 180, 160}
		bw := float64(2)
		vector.FillRect(screen, float32(imgX), float32(imgY), float32(imgW), float32(bw), c, false)
		vector.FillRect(screen, float32(imgX), float32(imgY+imgH-bw), float32(imgW), float32(bw), c, false)
		vector.FillRect(screen, float32(imgX), float32(imgY), float32(bw), float32(imgH), c, false)
		vector.FillRect(screen, float32(imgX+imgW-bw), float32(imgY), float32(bw), float32(imgH), c, false)
	case i == ov.hoverIdx:
		c := color.RGBA{255, 255, 255, 120}
		bw := float64(1)
		vector.FillRect(screen, float32(imgX), float32(imgY), float32(imgW), float32(bw), c, false)
		vector.FillRect(screen, float32(imgX), float32(imgY+imgH-bw), float32(imgW), float32(bw), c, false)
		vector.FillRect(screen, float32(imgX), float32(imgY), float32(bw), float32(imgH), c, false)
		vector.FillRect(screen, float32(imgX+imgW-bw), float32(imgY), float32(bw), float32(imgH), c, false)
	}
}
