package app

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"runtime/debug"
	"sort"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/vector"

	"github.com/gethash/boozle/internal/ipc"
	"github.com/gethash/boozle/internal/pdf"
)

const (
	ovEnterFrames = 25
	ovExitFrames  = 18

	overviewMaxThumbPixels       = 180
	overviewThumbsPerSelection   = 32
	overviewThumbNeighborhoodRow = 2
	overviewSimpleAnimSlides     = 120

	// overviewPreviewDelay is how long the selection must rest before the
	// preview pane renders it at full preview resolution, so arrowing
	// through the grid doesn't queue a render per slide passed.
	overviewPreviewDelay = 120 * time.Millisecond
)

type ovPhase int

const (
	ovOff      ovPhase = iota
	ovEntering         // anim 0 → 1
	ovActive
	ovExiting // anim 0 → 1
)

// overview is the slide grid opened with Tab: a large preview of the
// selected slide on the left and a thumbnail grid on the right.
//
// The audience window owns the overview state and handles its input. In
// presenter mode the presenter process mirrors that state over IPC and does
// all the drawing (and thumbnail loading), so the audience never sees it.
type overview struct {
	phase    ovPhase
	anim     float64
	pageList []int
	bufW     int
	bufH     int

	cols     int
	cellW    float64
	cellH    float64
	padding  float64
	gridX    int
	gridY    int
	cells    []overviewCell
	gridRect presenterRect // area covered by gridLayer

	// Thumbnails. store is nil when this window doesn't load thumbnails.
	store  *overviewThumbStore
	byPage map[int][]int // page → list indices (play lists may repeat pages)
	thumbW int
	thumbH int
	thumbs []*ebiten.Image // indexed by list index; nil = not loaded; non-nil = pinned in store

	gridLayer  *ebiten.Image
	gridAll    bool  // redraw every tile into gridLayer
	dirtyTiles []int // tiles whose thumbnail arrived since the last draw
	version    int   // bumps whenever a thumbnail arrives (redraw detection)

	fromIdx   int // listIdx when overview was opened
	selIdx    int // keyboard-selected cell (list index)
	exitToIdx int // destination when closing
	hoverIdx  int // -1 = none
	initMx    int // cursor x when overview opened — hover is suppressed until cursor moves
	initMy    int

	loadSel      int // selection nearJobs was built for
	nearJobs     []pdf.Job
	selChangedAt time.Time

	labelSel int
	label    string
}

type overviewCell struct {
	x, y, w, h float64
}

type rectF struct {
	x, y, w, h float64
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
	leftW := max(1, bufW-margin*2-gap-rightW)
	fullH := max(1, bufH-margin*2)

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
func computeOvGrid(n, bufW, bufH int) (gridX, gridY, cols int, cellW, cellH, padding float64) {
	content := computeOverviewLayout(bufW, bufH).gridContent
	padding = 12.0
	if n == 0 {
		return content.x, content.y, 1, float64(content.w) - 2*padding, float64(content.h) - 2*padding, padding
	}
	aspect := float64(content.w) / float64(content.h)
	cols = max(1, int(math.Ceil(math.Sqrt(float64(n)*aspect))))
	rows := (n + cols - 1) / cols
	cellW = (float64(content.w) - padding*float64(cols+1)) / float64(cols)
	cellH = (float64(content.h) - padding*float64(rows+1)) / float64(rows)
	return content.x, content.y, cols, cellW, cellH, padding
}

// newOverview lays out an overview for pageList at the given buffer size,
// entering from list index from. Call attach to start loading thumbnails.
func newOverview(pageList []int, bufW, bufH, from int) overview {
	n := len(pageList)
	gridX, gridY, cols, cellW, cellH, padding := computeOvGrid(n, bufW, bufH)
	from = clampListIndex(from, n)
	ov := overview{
		phase:     ovEntering,
		pageList:  pageList,
		bufW:      bufW,
		bufH:      bufH,
		cols:      cols,
		cellW:     cellW,
		cellH:     cellH,
		padding:   padding,
		gridX:     gridX,
		gridY:     gridY,
		gridRect:  computeOverviewLayout(bufW, bufH).gridContent,
		thumbW:    max(1, min(overviewMaxThumbPixels, int(math.Round(cellW)))),
		thumbH:    max(1, min(overviewMaxThumbPixels, int(math.Round(cellH)))),
		thumbs:    make([]*ebiten.Image, n),
		gridAll:   true,
		fromIdx:   from,
		selIdx:    from,
		exitToIdx: from,
		hoverIdx:  -1,
		loadSel:   -1,
		labelSel:  -1,
	}
	ov.buildCells(n)
	ov.initMx, ov.initMy = ebiten.CursorPosition()
	return ov
}

// attach makes this overview load thumbnails through p: cached ones are
// shown immediately, the rest are queued as background renders. ov must
// not move in memory while attached (p keeps a callback into it).
func (ov *overview) attach(p *pipeline) {
	ov.store = p.thumbs
	ov.byPage = make(map[int][]int, len(ov.pageList))
	for i, page := range ov.pageList {
		ov.byPage[page] = append(ov.byPage[page], i)
	}
	p.onThumb = ov.receiveThumb
	p.thumbs.fitTo(ov.thumbW, ov.thumbH, len(ov.byPage))
	var background []pdf.Job
	for i, page := range ov.pageList {
		if ov.byPage[page][0] != i {
			continue // repeated page: one job covers all its tiles
		}
		key := ov.thumbKey(page)
		if img, ok := p.thumbs.Get(key); ok {
			ov.receiveThumb(key, img)
			continue
		}
		background = append(background, pdf.Job{Kind: pdf.KindThumb, Key: key})
	}
	p.sched.AddBackground(background...)
}

// release unpins thumbnails, frees the grid layer and cancels queued
// thumbnail work. Safe on a zero overview and with a nil pipeline.
func (ov *overview) release(p *pipeline) {
	if ov.store != nil {
		for i, img := range ov.thumbs {
			if img != nil {
				ov.store.Unpin(ov.thumbKey(ov.pageList[i]))
			}
		}
	}
	if ov.gridLayer != nil {
		ov.gridLayer.Deallocate()
		ov.gridLayer = nil
	}
	if p != nil && ov.store != nil {
		p.onThumb = nil
		p.sched.ClearBackground()
		// Thumbnail loading churns through pixel buffers and PNG decodes;
		// hand that memory back to the OS once the overview is gone.
		go debug.FreeOSMemory()
	}
	*ov = overview{}
}

func (ov *overview) thumbKey(page int) pdf.CacheKey {
	return pdf.CacheKey{Page: page, W: ov.thumbW, H: ov.thumbH}
}

// receiveThumb assigns a freshly available thumbnail to every tile showing
// its page and pins it for as long as the overview is open.
func (ov *overview) receiveThumb(key pdf.CacheKey, img *ebiten.Image) {
	if ov.phase == ovOff || ov.store == nil || key.W != ov.thumbW || key.H != ov.thumbH {
		return
	}
	for _, i := range ov.byPage[key.Page] {
		if ov.thumbs[i] != nil {
			continue
		}
		ov.store.Pin(key)
		ov.thumbs[i] = img
		ov.dirtyTiles = append(ov.dirtyTiles, i)
		ov.version++
	}
}

// visibleJobs returns render work that should run before background
// thumbnails: the selected slide at preview size, once the selection has
// rested for overviewPreviewDelay, and the thumbnails around the selection.
func (ov *overview) visibleJobs(p *pipeline, now time.Time) []pdf.Job {
	if ov.store == nil || (ov.phase != ovEntering && ov.phase != ovActive) {
		return nil
	}
	if ov.loadSel != ov.selIdx {
		ov.loadSel = ov.selIdx
		ov.selChangedAt = now
		ov.nearJobs = ov.nearJobs[:0]
		for _, idx := range overviewLoadOrder(len(ov.pageList), ov.selIdx, ov.fromIdx, ov.cols) {
			ov.nearJobs = append(ov.nearJobs, pdf.Job{Kind: pdf.KindThumb, Key: ov.thumbKey(ov.pageList[idx])})
		}
	}
	var jobs []pdf.Job
	if now.Sub(ov.selChangedAt) >= overviewPreviewDelay {
		if key, ok := ov.previewKey(p, ov.selIdx); ok {
			jobs = append(jobs, p.missing(key)...)
		}
	}
	for _, j := range ov.nearJobs {
		if !p.thumbs.Has(j.Key) {
			jobs = append(jobs, j)
		}
	}
	return jobs
}

func (ov *overview) previewKey(p *pipeline, idx int) (pdf.CacheKey, bool) {
	if idx < 0 || idx >= len(ov.pageList) {
		return pdf.CacheKey{}, false
	}
	c := computeOverviewLayout(ov.bufW, ov.bufH).previewContent
	key, _, _, ok := p.fit(ov.pageList[idx], c.w, c.h)
	return key, ok
}

// step advances the enter/exit animation by one frame. It returns true when
// the exit animation has finished.
func (ov *overview) step() (exited bool) {
	switch ov.phase {
	case ovEntering:
		ov.anim += 1.0 / ovEnterFrames
		if ov.anim >= 1 {
			ov.anim = 1
			ov.phase = ovActive
		}
	case ovExiting:
		ov.anim += 1.0 / ovExitFrames
		if ov.anim >= 1 {
			return true
		}
	}
	return false
}

func (ov *overview) cellRect(i int) (x, y, w, h float64) {
	if i >= 0 && i < len(ov.cells) {
		c := ov.cells[i]
		return c.x, c.y, c.w, c.h
	}
	return 0, 0, 0, 0
}

func (ov *overview) buildCells(n int) {
	ov.cells = make([]overviewCell, n)
	for i := range n {
		col := i % ov.cols
		row := i / ov.cols
		x := float64(ov.gridX) + ov.padding + float64(col)*(ov.cellW+ov.padding)
		y := float64(ov.gridY) + ov.padding + float64(row)*(ov.cellH+ov.padding)
		ov.cells[i] = overviewCell{x: x, y: y, w: ov.cellW, h: ov.cellH}
	}
}

func (ov *overview) hoverIndex(mx, my, n int) int {
	if n <= 0 || ov.cols <= 0 {
		return -1
	}
	fx := float64(mx) - float64(ov.gridX) - ov.padding
	fy := float64(my) - float64(ov.gridY) - ov.padding
	if fx < 0 || fy < 0 {
		return -1
	}
	stepX := ov.cellW + ov.padding
	stepY := ov.cellH + ov.padding
	if stepX <= 0 || stepY <= 0 {
		return -1
	}
	col := int(fx / stepX)
	row := int(fy / stepY)
	localX := fx - float64(col)*stepX
	localY := fy - float64(row)*stepY
	if col < 0 || col >= ov.cols || localX >= ov.cellW || localY >= ov.cellH {
		return -1
	}
	idx := row*ov.cols + col
	if idx < 0 || idx >= n {
		return -1
	}
	return idx
}

// updateHover tracks the tile under the mouse (suppressed until the cursor
// moves after opening) and returns the clicked tile, or -1.
func (ov *overview) updateHover() (clicked int) {
	mx, my := ebiten.CursorPosition()
	ov.hoverIdx = -1
	if mx != ov.initMx || my != ov.initMy {
		ov.hoverIdx = ov.hoverIndex(mx, my, len(ov.pageList))
	}
	if inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft) && ov.hoverIdx >= 0 {
		return ov.hoverIdx
	}
	return -1
}

func (ov *overview) useStaticGrid(n int) bool {
	return n >= overviewSimpleAnimSlides || ov.phase == ovActive || ov.phase == ovExiting
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
	return min(1, max(0, t))
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

func clampListIndex(idx, n int) int {
	if n <= 0 {
		return 0
	}
	return max(0, min(n-1, idx))
}

// overviewLoadOrder returns up to overviewThumbsPerSelection list indices to
// load first: the selection, the originating slide, then the grid
// neighbourhood around the selection.
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

// ── audience-side control ────────────────────────────────────────────────

// openOverview lays out the overview. Thumbnails are only loaded when this
// window draws the overview (i.e. no presenter process owns it).
func (g *Game) openOverview() {
	g.ov = newOverview(g.pageList, g.bufW, g.bufH, g.listIdx)
	if g.shouldDrawOverviewOnAudience() && g.pipe != nil {
		g.ov.attach(g.pipe)
	}
}

// closeOverview starts the exit animation towards targetIdx and requests
// the full-size render of the target right away, so it is usually ready
// when the zoom-out finishes.
func (g *Game) closeOverview(targetIdx int) {
	g.ov.exitToIdx = targetIdx
	g.ov.phase = ovExiting
	g.ov.anim = 0
	if g.pipe != nil && g.ov.store != nil {
		g.pipe.onThumb = nil
		g.pipe.sched.ClearBackground()
		g.pipe.sched.SetVisible(nil)
	}
	if g.pipe != nil && g.bufW > 0 && g.bufH > 0 {
		if key, _, _, ok := g.pipe.fit(g.pageList[targetIdx], g.bufW, g.bufH); ok {
			g.pipe.requestNow(key)
		}
	}
}

// updateOverview drives animation, handles input, and finalises the transition.
// It is called from Update() whenever ov.phase != ovOff.
func (g *Game) updateOverview(presenterCmds []ipc.PresenterCommand) error {
	ov := &g.ov
	n := len(g.pageList)

	if ov.bufW != g.bufW || ov.bufH != g.bufH {
		g.relayoutOverview()
	}

	switch ov.phase {
	case ovEntering:
		ov.step()

	case ovActive:
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
		for _, cmd := range presenterCmds {
			if cmd.Name == presenterCmdOverviewGo && cmd.Arg >= 0 && cmd.Arg < n {
				g.closeOverview(cmd.Arg)
				return nil
			}
		}
		if g.shouldDrawOverviewOnAudience() {
			if clicked := ov.updateHover(); clicked >= 0 {
				g.closeOverview(clicked)
				return nil
			}
		}

	case ovExiting:
		if ov.step() {
			exitTo, from := ov.exitToIdx, ov.fromIdx
			ov.release(g.pipe)
			// The zoom-out was the transition; just record the jump.
			if exitTo != from {
				g.prevListIdx = from
				g.listIdx = exitTo
				g.auto.Reset(g.currentPage() + 1)
			}
		}
	}

	if g.pipe != nil && ov.store != nil {
		g.pipe.sched.SetVisible(ov.visibleJobs(g.pipe, time.Now()))
	}
	return nil
}

// relayoutOverview rebuilds the grid after a window resize, keeping the
// animation phase and selection.
func (g *Game) relayoutOverview() {
	old := g.ov
	attached := old.store != nil
	old.release(g.pipe)
	g.ov = newOverview(g.pageList, g.bufW, g.bufH, old.fromIdx)
	g.ov.phase, g.ov.anim = old.phase, old.anim
	g.ov.selIdx, g.ov.exitToIdx = old.selIdx, old.exitToIdx
	if attached {
		g.ov.attach(g.pipe)
	}
}

// ── drawing (shared by audience and presenter windows) ──────────────────

// draw renders the overview over screen. p may be nil (no thumbnails or
// preview renders); cur is the slide shown before the overview opened and
// curRect where it sat on screen, which the preview animates from.
func (ov *overview) draw(screen *ebiten.Image, p *pipeline, cur *ebiten.Image, curRect rectF) {
	n := len(ov.pageList)
	if n == 0 {
		return
	}
	var scrimAlpha float64
	switch ov.phase {
	case ovEntering:
		scrimAlpha = lerpF(0, 230, easeInOut(ov.anim))
	case ovActive:
		scrimAlpha = 230
	case ovExiting:
		scrimAlpha = lerpF(230, 0, easeInOut(ov.anim))
	}
	vector.FillRect(screen, 0, 0, float32(ov.bufW), float32(ov.bufH),
		color.RGBA{8, 10, 18, uint8(scrimAlpha)}, false)

	lo := computeOverviewLayout(ov.bufW, ov.bufH)
	drawPresenterPanel(screen, lo.previewPanel)
	drawPresenterPanel(screen, lo.gridPanel)
	drawPresenterText(screen, "SELECTED", lo.previewPanel.x+18, lo.previewPanel.y+14, 2, colorLabel)
	drawPresenterText(screen, "OVERVIEW", lo.gridPanel.x+18, lo.gridPanel.y+14, 2, colorLabel)

	ov.drawPreview(screen, p, cur, curRect, lo.previewContent)
	if ov.useStaticGrid(n) {
		ov.drawGridLayer(screen)
		if ov.phase == ovActive {
			for _, i := range []int{ov.fromIdx, ov.selIdx, ov.hoverIdx} {
				ov.drawTileBorder(screen, i, ov.tileImageRect(i))
			}
		}
	} else {
		for i := range n {
			ov.drawTile(screen, i)
		}
	}

	if ov.phase == ovActive && ov.selIdx >= 0 && ov.selIdx < n {
		if ov.labelSel != ov.selIdx {
			ov.labelSel = ov.selIdx
			ov.label = fmt.Sprintf("p. %d / %d", ov.pageList[ov.selIdx]+1, n)
		}
		drawPresenterText(screen, ov.label, lo.previewPanel.x+18, lo.previewPanel.y+lo.previewPanel.h-32, 2, color.RGBA{248, 250, 252, 220})
	}
}

// previewImage picks the image for the left pane: the debounced
// preview-size render of the selection if it is ready, else the live slide
// for the originating tile, else any cached render of the selection, else
// its thumbnail.
func (ov *overview) previewImage(p *pipeline, cur *ebiten.Image) *ebiten.Image {
	idx := ov.selIdx
	switch ov.phase {
	case ovEntering:
		idx = ov.fromIdx
	case ovExiting:
		idx = ov.exitToIdx
	}
	key, keyOK := pdf.CacheKey{}, false
	if p != nil {
		key, keyOK = ov.previewKey(p, idx)
		if keyOK {
			if img, ok := p.slides.Get(key); ok {
				return img.(*ebiten.Image)
			}
		}
	}
	if idx == ov.fromIdx && cur != nil {
		return cur
	}
	if keyOK {
		if img, _ := p.resolve(key); img != nil {
			return img
		}
	}
	if idx >= 0 && idx < len(ov.thumbs) && ov.thumbs[idx] != nil {
		return ov.thumbs[idx]
	}
	return cur
}

// drawPreview draws the large slide on the left, animating in from the
// slide's normal position on enter and zooming to fill the screen on exit.
func (ov *overview) drawPreview(screen *ebiten.Image, p *pipeline, cur *ebiten.Image, curRect rectF, content presenterRect) {
	img := ov.previewImage(p, cur)
	if img == nil {
		return
	}
	iw := float64(img.Bounds().Dx())
	ih := float64(img.Bounds().Dy())
	if iw <= 0 || ih <= 0 {
		return
	}
	fit := imageFitRect(img, content)
	t := fit
	switch ov.phase {
	case ovEntering:
		start := curRect
		if start.w <= 0 || start.h <= 0 {
			start = fit
		}
		t = lerpRect(start, fit, easeInOut(clamp01(ov.anim/0.7)))
	case ovExiting:
		full := imageFitRect(img, presenterRect{w: ov.bufW, h: ov.bufH})
		t = lerpRect(fit, full, easeInOut(ov.anim))
	}
	if t.w < 1 || t.h < 1 {
		return
	}
	op := &ebiten.DrawImageOptions{}
	op.GeoM.Scale(t.w/iw, t.h/ih)
	op.GeoM.Translate(t.x, t.y)
	op.Filter = ebiten.FilterLinear
	// At rest the preview is drawn ~1:1 from its own render; strong
	// downscales only happen during the short zoom animations, which don't
	// justify a permanent mipmap chain (+33%) on full-screen slides.
	op.DisableMipmaps = true
	screen.DrawImage(img, op)
}

func lerpRect(a, b rectF, t float64) rectF {
	return rectF{x: lerpF(a.x, b.x, t), y: lerpF(a.y, b.y, t), w: lerpF(a.w, b.w, t), h: lerpF(a.h, b.h, t)}
}

func imageFitRect(img *ebiten.Image, r presenterRect) rectF {
	w, h, offX, offY := aspectFit(float64(img.Bounds().Dx()), float64(img.Bounds().Dy()), r.w, r.h)
	return rectF{x: float64(r.x + offX), y: float64(r.y + offY), w: float64(w), h: float64(h)}
}

// drawGridLayer composites the static grid. Tiles are cached in gridLayer
// (covering only the grid panel) and redrawn individually as thumbnails
// arrive, instead of repainting every tile.
func (ov *overview) drawGridLayer(screen *ebiten.Image) {
	r := ov.gridRect
	if r.w <= 0 || r.h <= 0 {
		return
	}
	if ov.gridLayer == nil {
		ov.gridLayer = ebiten.NewImageWithOptions(image.Rect(0, 0, r.w, r.h), &ebiten.NewImageOptions{Unmanaged: true})
		ov.gridAll = true
	}
	ox, oy := float64(r.x), float64(r.y)
	if ov.gridAll {
		ov.gridLayer.Clear()
		for i := range ov.pageList {
			x, y, w, h := ov.cellRect(i)
			drawOverviewTileBase(ov.gridLayer, ov.thumbs[i], x-ox, y-oy, w, h, 1)
		}
		ov.gridAll = false
	} else {
		for _, i := range ov.dirtyTiles {
			x, y, w, h := ov.cellRect(i)
			x, y = x-ox, y-oy
			cell := image.Rect(int(math.Floor(x)), int(math.Floor(y)), int(math.Ceil(x+w)), int(math.Ceil(y+h)))
			ov.gridLayer.SubImage(cell).(*ebiten.Image).Clear()
			drawOverviewTileBase(ov.gridLayer, ov.thumbs[i], x, y, w, h, 1)
		}
	}
	ov.dirtyTiles = ov.dirtyTiles[:0]

	alpha := 1.0
	switch ov.phase {
	case ovEntering:
		alpha = easeOut(ov.anim)
	case ovExiting:
		alpha = lerpF(1, 0, clamp01(ov.anim*2))
	}
	if alpha <= 0 {
		return
	}
	op := &ebiten.DrawImageOptions{}
	op.GeoM.Translate(ox, oy)
	op.ColorScale.ScaleAlpha(float32(alpha))
	screen.DrawImage(ov.gridLayer, op)
}

// drawTile draws one tile directly to screen with the per-tile cascade used
// while small decks enter.
func (ov *overview) drawTile(screen *ebiten.Image, i int) {
	n := len(ov.pageList)
	cx, cy, cw, ch := ov.cellRect(i)
	tx, ty, tw, th := cx, cy, cw, ch
	alpha := 1.0

	switch ov.phase {
	case ovEntering:
		maxDist := max(1, ov.fromIdx, n-1-ov.fromIdx)
		nd := float64(abs(i-ov.fromIdx)) / float64(maxDist)
		startT := 0.25 + nd*0.40
		localT := easeOut(clamp01((ov.anim - startT) / 0.35))
		tx = lerpF(cx+cw*0.5, cx, localT)
		ty = lerpF(cy+ch*0.5, cy, localT)
		tw = lerpF(0, cw, localT)
		th = lerpF(0, ch, localT)
		alpha = clamp01(localT * 1.5)
	case ovExiting:
		alpha = lerpF(1, 0, clamp01(ov.anim*2))
	}
	if tw < 1 || th < 1 || alpha <= 0 {
		return
	}
	r := drawOverviewTileBase(screen, ov.thumbs[i], tx, ty, tw, th, alpha)
	if ov.phase == ovActive {
		ov.drawTileBorder(screen, i, r)
	}
}

// drawOverviewTileBase draws a thumbnail aspect-fitted into the cell, or a
// placeholder until it loads, and returns where the image landed.
func drawOverviewTileBase(dst *ebiten.Image, img *ebiten.Image, tx, ty, tw, th, alpha float64) rectF {
	if img == nil {
		if a := uint8(alpha * 200); a > 0 {
			vector.FillRect(dst, float32(tx), float32(ty), float32(tw), float32(th),
				color.RGBA{31, 41, 55, a}, false)
		}
		return rectF{x: tx, y: ty, w: tw, h: th}
	}
	iw := float64(img.Bounds().Dx())
	ih := float64(img.Bounds().Dy())
	if iw <= 0 || ih <= 0 {
		return rectF{}
	}
	sc := math.Min(tw/iw, th/ih)
	r := rectF{w: iw * sc, h: ih * sc}
	r.x = tx + (tw-r.w)/2
	r.y = ty + (th-r.h)/2
	op := &ebiten.DrawImageOptions{}
	op.GeoM.Scale(sc, sc)
	op.GeoM.Translate(r.x, r.y)
	op.ColorScale.ScaleAlpha(float32(alpha))
	op.Filter = ebiten.FilterLinear
	dst.DrawImage(img, op)
	return r
}

// tileImageRect returns where tile i's thumbnail sits inside its cell (the
// whole cell while it is still a placeholder).
func (ov *overview) tileImageRect(i int) rectF {
	if i < 0 || i >= len(ov.thumbs) {
		return rectF{}
	}
	x, y, w, h := ov.cellRect(i)
	img := ov.thumbs[i]
	if img == nil {
		return rectF{x: x, y: y, w: w, h: h}
	}
	iw := float64(img.Bounds().Dx())
	ih := float64(img.Bounds().Dy())
	if iw <= 0 || ih <= 0 {
		return rectF{}
	}
	sc := math.Min(w/iw, h/ih)
	r := rectF{w: iw * sc, h: ih * sc}
	r.x = x + (w-r.w)/2
	r.y = y + (h-r.h)/2
	return r
}

// drawTileBorder highlights the selection (rainbow), the slide the overview
// was opened from (grey) and the hovered tile (white), flush with the image.
func (ov *overview) drawTileBorder(screen *ebiten.Image, i int, r rectF) {
	n := len(ov.pageList)
	if i < 0 || i >= n || r.w < 1 || r.h < 1 {
		return
	}
	switch {
	case i == ov.selIdx && i != ov.fromIdx:
		gradT := float32(0)
		if n > 1 {
			gradT = float32(i) / float32(n-1)
		}
		drawOverviewBorder(screen, r, 2, rainbowAt(gradT, 255))
	case i == ov.fromIdx:
		drawOverviewBorder(screen, r, 2, color.RGBA{180, 180, 180, 160})
	case i == ov.hoverIdx:
		drawOverviewBorder(screen, r, 1, color.RGBA{255, 255, 255, 120})
	}
}

func drawOverviewBorder(screen *ebiten.Image, r rectF, bw float64, c color.Color) {
	vector.FillRect(screen, float32(r.x), float32(r.y), float32(r.w), float32(bw), c, false)
	vector.FillRect(screen, float32(r.x), float32(r.y+r.h-bw), float32(r.w), float32(bw), c, false)
	vector.FillRect(screen, float32(r.x), float32(r.y), float32(bw), float32(r.h), c, false)
	vector.FillRect(screen, float32(r.x+r.w-bw), float32(r.y), float32(bw), float32(r.h), c, false)
}
