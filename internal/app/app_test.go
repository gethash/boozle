package app

import (
	"image"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/gethash/boozle/internal/config"
	"github.com/gethash/boozle/internal/ipc"
	"github.com/gethash/boozle/internal/pdf"
	"github.com/gethash/boozle/internal/timer"
)

func TestBuildPageList(t *testing.T) {
	all := buildPageList(4, config.Config{PageRange: config.PageRange{All: true}})
	if !reflect.DeepEqual(all, []int{0, 1, 2, 3}) {
		t.Fatalf("all pages = %v, want [0 1 2 3]", all)
	}

	filtered := buildPageList(5, config.Config{PageRange: config.PageRange{Pages: []int{2, 4, 9}}})
	if !reflect.DeepEqual(filtered, []int{1, 3}) {
		t.Fatalf("filtered pages = %v, want [1 3]", filtered)
	}

	playback := buildPageList(5, config.Config{
		PageRange:        config.PageRange{All: true},
		PlaybackPages:    []int{1, 1, 4, 9, 3},
		UsePlaybackPages: true,
	})
	if !reflect.DeepEqual(playback, []int{0, 0, 3, 2}) {
		t.Fatalf("playback pages = %v, want [0 0 3 2]", playback)
	}

	emptyPlayback := buildPageList(5, config.Config{
		PageRange:        config.PageRange{All: true},
		UsePlaybackPages: true,
	})
	if len(emptyPlayback) != 0 {
		t.Fatalf("empty playback pages = %v, want []", emptyPlayback)
	}
}

func TestInitialIndex(t *testing.T) {
	pageList := []int{0, 2, 4, 7}
	tests := []struct {
		name      string
		startPage int
		want      int
	}{
		{"exact", 5, 2},
		{"nearest later", 4, 2},
		{"before first", 1, 0},
		{"after last", 99, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := initialIndex(pageList, tt.startPage); got != tt.want {
				t.Fatalf("initialIndex(%v, %d) = %d, want %d", pageList, tt.startPage, got, tt.want)
			}
		})
	}
}

func TestAdvanceStopsAtNonLoopBoundaries(t *testing.T) {
	tests := []struct {
		name  string
		start int
		delta int
	}{
		{"left at first slide", 0, -1},
		{"right at last slide", 2, +1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &Game{
				pageList:    []int{0, 1, 2},
				listIdx:     tt.start,
				prevListIdx: 99,
				lastNavDir:  42,
			}

			g.advance(tt.delta)

			if g.listIdx != tt.start {
				t.Fatalf("listIdx = %d, want unchanged %d", g.listIdx, tt.start)
			}
			if g.prevListIdx != 99 {
				t.Fatalf("prevListIdx = %d, want unchanged 99", g.prevListIdx)
			}
			if g.lastNavDir != 42 {
				t.Fatalf("lastNavDir = %d, want unchanged 42", g.lastNavDir)
			}
		})
	}
}

func TestAdvanceWrapsWhenLooping(t *testing.T) {
	tests := []struct {
		name  string
		start int
		delta int
		want  int
		dir   int
	}{
		{"left from first slide", 0, -1, 2, -1},
		{"right from last slide", 2, +1, 0, +1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &Game{
				cfg:      config.Config{Loop: true},
				pageList: []int{0, 1, 2},
				listIdx:  tt.start,
			}

			g.advance(tt.delta)

			if g.listIdx != tt.want {
				t.Fatalf("listIdx = %d, want %d", g.listIdx, tt.want)
			}
			if g.prevListIdx != tt.start {
				t.Fatalf("prevListIdx = %d, want %d", g.prevListIdx, tt.start)
			}
			if g.lastNavDir != tt.dir {
				t.Fatalf("lastNavDir = %d, want %d", g.lastNavDir, tt.dir)
			}
		})
	}
}

func TestAdvanceAutoQuitsPastLastPage(t *testing.T) {
	g := &Game{
		cfg:         config.Config{AutoQuit: true},
		pageList:    []int{0, 1, 2},
		listIdx:     2,
		prevListIdx: 99,
		lastNavDir:  42,
	}

	g.advance(+1)

	if !g.quit {
		t.Fatal("quit = false, want true")
	}
	if g.listIdx != 2 {
		t.Fatalf("listIdx = %d, want unchanged 2", g.listIdx)
	}
	if g.prevListIdx != 99 {
		t.Fatalf("prevListIdx = %d, want unchanged 99", g.prevListIdx)
	}
	if g.lastNavDir != 42 {
		t.Fatalf("lastNavDir = %d, want unchanged 42", g.lastNavDir)
	}
}

func TestAdvanceUpdatesNavigationStateOnMove(t *testing.T) {
	g := &Game{
		pageList:    []int{0, 0, 3},
		listIdx:     0,
		prevListIdx: 99,
	}

	g.advance(+1)

	if g.listIdx != 1 {
		t.Fatalf("listIdx = %d, want 1", g.listIdx)
	}
	if g.prevListIdx != 0 {
		t.Fatalf("prevListIdx = %d, want 0", g.prevListIdx)
	}
	if g.lastNavDir != 1 {
		t.Fatalf("lastNavDir = %d, want 1", g.lastNavDir)
	}
}

func TestValidatePresenterMonitorAllowsSameMonitorWindowedOnly(t *testing.T) {
	err := validatePresenterMonitor(config.Config{MonitorIdx: 0, PresenterMonitor: 0})
	if err == nil {
		t.Fatal("same monitor fullscreen should fail")
	}
	err = validatePresenterMonitor(config.Config{MonitorIdx: 0, PresenterMonitor: 0, NoFullscreen: true})
	if err != nil {
		t.Fatalf("same monitor with --no-fullscreen should work: %v", err)
	}
	err = validatePresenterMonitor(config.Config{MonitorIdx: 0, PresenterMonitor: 1})
	if err != nil {
		t.Fatalf("different monitors should work: %v", err)
	}
}

func TestPresenterCommandHelpers(t *testing.T) {
	cmds := []ipc.PresenterCommand{
		{Name: presenterCmdLeft},
		{Name: presenterCmdDigit, Arg: 7},
	}
	if !presenterCmdPressed(cmds, presenterCmdLeft) {
		t.Fatal("presenterCmdPressed did not find existing command")
	}
	if presenterCmdPressed(cmds, presenterCmdRight) {
		t.Fatal("presenterCmdPressed found missing command")
	}

	g := &Game{}
	g.appendDigit(1)
	g.appendDigit(2)
	g.appendDigit(99)
	for i := 3; i <= 8; i++ {
		g.appendDigit(i)
	}
	if g.digitBuf != "345678" {
		t.Fatalf("digitBuf = %q, want last six digits 345678", g.digitBuf)
	}
}

func TestAutoProgressFraction(t *testing.T) {
	a := timer.New(10*time.Second, nil)
	g := &Game{auto: a}
	if got := g.autoProgressFraction(); got != 0 {
		t.Fatalf("inactive fraction = %v, want 0", got)
	}

	a = timer.New(10*time.Second, nil)
	a.Reset(1)
	a.TogglePause(1)
	g.auto = a
	if got := g.autoProgressFraction(); got != a.FractionAtPause() {
		t.Fatalf("paused fraction = %v, want pause fraction %v", got, a.FractionAtPause())
	}
}

func TestBroadcastStateIncludesPresenterMetadata(t *testing.T) {
	a := timer.New(10*time.Second, nil)
	a.Reset(3)
	g := &Game{
		auto:      a,
		pageList:  []int{2, 4, 6},
		listIdx:   1,
		startedAt: time.Now().Add(-90 * time.Second),
		stateCh:   make(chan ipc.PresenterState, 1),
	}

	g.broadcastState()
	st := <-g.stateCh
	if st.Page != 4 || st.ListIndex != 1 || st.NextPage != 6 {
		t.Fatalf("state page metadata = %+v, want page=4 index=1 next=6", st)
	}
	if st.FractionPerSec != 0.1 {
		t.Fatalf("FractionPerSec = %v, want 0.1 for a 10s auto-advance", st.FractionPerSec)
	}

	g.listIdx = 2
	g.broadcastState()
	if st := <-g.stateCh; st.NextPage != -1 {
		t.Fatalf("NextPage on last slide without --loop = %d, want -1", st.NextPage)
	}
	g.cfg.Loop = true
	g.broadcastState()
	if st := <-g.stateCh; st.NextPage != 2 {
		t.Fatalf("NextPage on last slide with --loop = %d, want first page 2", st.NextPage)
	}
}

func TestBroadcastStateOnlySendsChanges(t *testing.T) {
	a := timer.New(10*time.Second, nil)
	a.Reset(1)
	g := &Game{
		auto:     a,
		pageList: []int{0, 1, 2},
		stateCh:  make(chan ipc.PresenterState, 1),
	}
	g.broadcastState()
	<-g.stateCh

	// The auto-advance fraction moves every frame but is extrapolated by
	// the presenter, so it alone must not trigger a resend.
	time.Sleep(5 * time.Millisecond)
	g.broadcastState()
	select {
	case st := <-g.stateCh:
		t.Fatalf("unchanged state was resent: %+v", st)
	default:
	}

	g.listIdx = 1
	g.broadcastState()
	select {
	case st := <-g.stateCh:
		if st.ListIndex != 1 {
			t.Fatalf("ListIndex = %d, want 1", st.ListIndex)
		}
	default:
		t.Fatal("changed state was not sent")
	}
}

func TestBroadcastStateIncludesOverviewSnapshot(t *testing.T) {
	g := &Game{
		auto:      timer.New(0, nil),
		pageList:  []int{0, 0, 3, 2},
		listIdx:   2,
		startedAt: time.Now(),
		stateCh:   make(chan ipc.PresenterState, 1),
		ov: overview{
			phase:     ovActive,
			anim:      1,
			fromIdx:   1,
			selIdx:    3,
			exitToIdx: 3,
		},
	}

	g.broadcastState()
	st := <-g.stateCh
	if st.Overview != (ipc.PresenterOverviewState{
		Active:        true,
		Phase:         int(ovActive),
		Anim:          1,
		FromIndex:     1,
		SelectedIndex: 3,
		ExitToIndex:   3,
	}) {
		t.Fatalf("Overview = %+v", st.Overview)
	}
}

func TestAudienceOverviewDrawRouting(t *testing.T) {
	g := &Game{ov: overview{phase: ovActive}}
	if !g.shouldDrawOverviewOnAudience() {
		t.Fatal("single-window mode should draw overview on the audience window")
	}
	g.stateCh = make(chan ipc.PresenterState, 1)
	if g.shouldDrawOverviewOnAudience() {
		t.Fatal("presenter mode should keep overview off the audience window")
	}
}

func TestLayoutHelpers(t *testing.T) {
	lo := (&PresenterGame{bufW: 1920, bufH: 1080}).presenterLayout()
	if lo.currentPanel.w <= lo.nextPanel.w {
		t.Fatalf("presenter current panel width = %d, next = %d; want current wider", lo.currentPanel.w, lo.nextPanel.w)
	}
	if lo.statusPanel.y <= lo.nextPanel.y {
		t.Fatalf("status panel should sit below next panel: next=%+v status=%+v", lo.nextPanel, lo.statusPanel)
	}

	ov := computeOverviewLayout(1920, 1080)
	if ov.previewPanel.x >= ov.gridPanel.x {
		t.Fatalf("overview preview should be left of grid: preview=%+v grid=%+v", ov.previewPanel, ov.gridPanel)
	}
	gridX, gridY, cols, cellW, cellH, _ := computeOvGrid(12, 1920, 1080)
	if gridX != ov.gridContent.x || gridY != ov.gridContent.y {
		t.Fatalf("grid origin = %d,%d, want %d,%d", gridX, gridY, ov.gridContent.x, ov.gridContent.y)
	}
	if cols <= 0 || cellW <= 0 || cellH <= 0 {
		t.Fatalf("invalid grid cols=%d cell=%fx%f", cols, cellW, cellH)
	}
}

func TestOverviewLoadOrderIsBoundedAndPrioritized(t *testing.T) {
	got := overviewLoadOrder(500, 250, 12, 20)
	if len(got) == 0 {
		t.Fatal("overviewLoadOrder returned no thumbnails")
	}
	if got[0] != 250 {
		t.Fatalf("first thumbnail = %d, want selected index 250", got[0])
	}
	if len(got) > overviewThumbsPerSelection {
		t.Fatalf("overviewLoadOrder returned %d thumbnails, want at most %d", len(got), overviewThumbsPerSelection)
	}
	foundInclude := false
	seen := map[int]bool{}
	for _, idx := range got {
		if idx < 0 || idx >= 500 {
			t.Fatalf("thumbnail index %d out of range", idx)
		}
		if seen[idx] {
			t.Fatalf("duplicate thumbnail index %d", idx)
		}
		seen[idx] = true
		if idx == 12 {
			foundInclude = true
		}
	}
	if !foundInclude {
		t.Fatal("overviewLoadOrder should include the originating slide")
	}
}

func TestOverviewAttachQueuesEveryPageOnce(t *testing.T) {
	p := &pipeline{
		thumbs: newOverviewThumbStore(""),
		sched:  pdf.NewScheduler(nil, nil),
	}
	pageList := make([]int, 97)
	for i := range pageList {
		pageList[i] = i % 90 // a play list may repeat pages
	}
	g := &Game{pageList: pageList, bufW: 1920, bufH: 1080}
	g.ov = newOverview(pageList, 1920, 1080, 0)
	g.ov.attach(p)

	if _, _, _, bg := p.sched.QueueLens(); bg != 90 {
		t.Fatalf("background thumbnail jobs = %d, want one per distinct page (90)", bg)
	}
	if p.onThumb == nil {
		t.Fatal("attach should route arriving thumbnails to the overview")
	}
	g.ov.release(p)
	if _, _, _, bg := p.sched.QueueLens(); bg != 0 {
		t.Fatalf("release left %d background jobs queued", bg)
	}
	if p.onThumb != nil || g.ov.phase != ovOff {
		t.Fatal("release should detach and reset the overview")
	}
}

func TestOverviewHoverIndexMatchesCellScan(t *testing.T) {
	const total = 500
	gridX, gridY, cols, cellW, cellH, padding := computeOvGrid(total, 1920, 1080)
	ov := overview{cols: cols, cellW: cellW, cellH: cellH, padding: padding, gridX: gridX, gridY: gridY}
	ov.buildCells(total)

	for _, idx := range []int{0, 7, 123, 499} {
		x, y, w, h := ov.cellRect(idx)
		mx := int(x + w/2)
		my := int(y + h/2)
		if got := ov.hoverIndex(mx, my, total); got != idx {
			t.Fatalf("hoverIndex at cell %d = %d", idx, got)
		}
	}
	if got := ov.hoverIndex(gridX-1, gridY-1, total); got != -1 {
		t.Fatalf("hoverIndex outside grid = %d, want -1", got)
	}
}

func TestOverviewLargeDeckUsesStaticGridAnimation(t *testing.T) {
	ov := overview{phase: ovEntering}
	if ov.useStaticGrid(overviewSimpleAnimSlides - 1) {
		t.Fatal("small entering deck should keep per-tile animation")
	}
	if !ov.useStaticGrid(overviewSimpleAnimSlides) {
		t.Fatal("large entering deck should use static grid animation")
	}
	ov.phase = ovActive
	if !ov.useStaticGrid(12) {
		t.Fatal("active overview should use the cached static grid")
	}
}

func TestPresenterModeOverviewDoesNotStartMasterThumbnails(t *testing.T) {
	g := &Game{
		stateCh:  make(chan ipc.PresenterState, 1),
		pageList: []int{0, 1, 2},
		listIdx:  1,
		bufW:     1280,
		bufH:     720,
		pipe:     &pipeline{thumbs: newOverviewThumbStore(""), sched: pdf.NewScheduler(nil, nil)},
	}
	g.openOverview()
	if g.ov.phase != ovEntering {
		t.Fatalf("overview phase = %v, want entering", g.ov.phase)
	}
	if g.ov.store != nil || g.pipe.onThumb != nil {
		t.Fatal("master overview must not load thumbnails when the presenter view draws the overview")
	}
	if _, _, _, bg := g.pipe.sched.QueueLens(); bg != 0 {
		t.Fatalf("master queued %d thumbnail jobs in presenter mode", bg)
	}
}

func TestOverviewDiskThumbCacheInvalidatesByPDFAndDimensions(t *testing.T) {
	t.Setenv("BOOZLE_CACHE_DIR", t.TempDir())
	dir := t.TempDir()
	pdfPath := filepath.Join(dir, "deck.pdf")
	if err := os.WriteFile(pdfPath, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := newOverviewDiskThumbCache(pdfPath)
	key := pdf.CacheKey{Page: 3, W: 120, H: 90}
	img := image.NewRGBA(image.Rect(0, 0, key.W, key.H))
	cache.Save(key, img)
	if _, ok := cache.Load(key); !ok {
		t.Fatal("saved thumbnail should load")
	}
	if _, ok := cache.Load(pdf.CacheKey{Page: 3, W: 121, H: 90}); ok {
		t.Fatal("different thumbnail dimensions should miss")
	}

	if err := os.WriteFile(pdfPath, []byte("two-two"), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(pdfPath, later, later); err != nil {
		t.Fatal(err)
	}
	cache = newOverviewDiskThumbCache(pdfPath)
	if _, ok := cache.Load(key); ok {
		t.Fatal("changed PDF size/modtime should invalidate old thumbnail")
	}
}

func TestAutoBudget(t *testing.T) {
	// Tiny window: clamps to floor.
	if got := autoBudget(320, 240); got != cacheBudgetMin {
		t.Errorf("autoBudget(320,240) = %d, want floor %d", got, cacheBudgetMin)
	}

	// Mid-size: cacheBudgetPages textures (1080p → 2048² textures), clear
	// of both floor and ceiling.
	const w, h = 1920, 1080
	want := pdf.TextureBytes(w, h) * cacheBudgetPages
	if got := autoBudget(w, h); got != want {
		t.Errorf("autoBudget(%d,%d) = %d, want %d", w, h, got, want)
	}

	// 4K: clamps to the ceiling.
	if got := autoBudget(3840, 2160); got != cacheBudgetMax {
		t.Errorf("autoBudget(3840,2160) = %d, want ceiling %d", got, cacheBudgetMax)
	}

	// 5K and up: the ceiling yields to three pages.
	if got, want := autoBudget(5120, 2880), 3*pdf.TextureBytes(5120, 2880); got != want {
		t.Errorf("autoBudget(5120,2880) = %d, want three pages %d", got, want)
	}

	// Invalid input returns the floor without panicking.
	if got := autoBudget(0, 0); got != cacheBudgetMin {
		t.Errorf("autoBudget(0,0) = %d, want floor %d", got, cacheBudgetMin)
	}
}

func TestTransitionAndElapsedHelpers(t *testing.T) {
	if parseTransStyle("slide") != transSlide {
		t.Fatal("slide should parse to transSlide")
	}
	if parseTransStyle("fade") != transFade {
		t.Fatal("fade should parse to transFade")
	}
	if parseTransStyle("none") != transNone || parseTransStyle("") != transNone {
		t.Fatal("none/empty should parse to transNone")
	}

	tr := transition{frame: 9, frames: 18}
	if got := tr.progress(); got != 0.5 {
		t.Fatalf("progress = %v, want 0.5", got)
	}
	if got := formatElapsed(3661); got != "01:01:01" {
		t.Fatalf("formatElapsed = %q, want 01:01:01", got)
	}
	if got := sign(-42); got != -1 {
		t.Fatalf("sign(-42) = %d, want -1", got)
	}
}

func TestWrapPresenterLinesIsRuneAware(t *testing.T) {
	got := wrapPresenterLines("Größe ändern\n\nÜberlänge-ohne-Leerzeichen", 6)
	want := []string{"Größe", "ändern", "", "Überlä", "nge-oh", "ne-Lee", "rzeich", "en"}
	if len(got) != len(want) {
		t.Fatalf("wrap = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("wrap = %q, want %q", got, want)
		}
	}
}

func TestDebugFontTextFoldsTypography(t *testing.T) {
	in := "Größe \u2013 \u201cquoted\u201d \u2026 \u2022 5\u20ac \u4e2d"
	if got, want := debugFontText(in), `Größe - "quoted" ... * 5EUR ?`; got != want {
		t.Fatalf("debugFontText = %q, want %q", got, want)
	}
	if s := "plain Ä text"; debugFontText(s) != s {
		t.Fatal("Latin-1 text should pass through unchanged")
	}
}

func TestRedrawGate(t *testing.T) {
	var g redrawGate[int]
	now := time.Now()
	if !g.needed(1, now) {
		t.Fatal("first frame must draw")
	}
	if g.needed(1, now.Add(10*time.Millisecond)) {
		t.Fatal("unchanged frame should be skipped")
	}
	if !g.needed(2, now.Add(20*time.Millisecond)) {
		t.Fatal("changed frame must draw")
	}
	if !g.needed(2, now.Add(2*time.Second)) {
		t.Fatal("a redraw is forced at least once per second")
	}
}

func TestPresenterArgsForwardTuningFlags(t *testing.T) {
	cfg := config.Config{PDFPath: "deck.pdf", PresenterMonitor: 1, NoFullscreen: true, CacheMB: 64, RenderScale: 0.75, DebugStats: true}
	got := presenterArgs(cfg, "/tmp/x.sock")
	want := []string{"--monitor", "1", "--_presenter-socket", "/tmp/x.sock", "--no-fullscreen", "--cache-mb", "64", "--render-scale", "0.75", "--debug-stats", "deck.pdf"}
	if len(got) != len(want) {
		t.Fatalf("args = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args = %q, want %q", got, want)
		}
	}
}

func TestPruneThumbDirByAgeAndSize(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	write := func(name string, size int, age time.Duration) {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
		mod := now.Add(-age)
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	write("expired.png", 10, 40*24*time.Hour)
	write("oldest.png", 100, 3*time.Hour)
	write("older.png", 100, 2*time.Hour)
	write("newest.png", 100, time.Hour)
	write("unrelated.txt", 1000, 90*24*time.Hour)

	pruneThumbDir(dir, now, 30*24*time.Hour, 250)

	for name, want := range map[string]bool{
		"expired.png":   false, // too old
		"oldest.png":    false, // LRU victim to get under 250 bytes
		"older.png":     true,
		"newest.png":    true,
		"unrelated.txt": true, // only thumbnails are pruned
	} {
		_, err := os.Stat(filepath.Join(dir, name))
		if got := err == nil; got != want {
			t.Errorf("%s exists = %v, want %v", name, got, want)
		}
	}
}

// TestPrefetchFitsCacheBudget checks that the slides kept around while
// navigating — current, one behind and the prefetched ones ahead — fit the
// automatic cache budget, so prefetched slides aren't evicted before use.
func TestPrefetchFitsCacheBudget(t *testing.T) {
	for _, sz := range [][2]int{{1280, 800}, {1920, 1080}, {2560, 1440}, {2880, 1800}, {3840, 2160}, {5120, 2880}} {
		page := pdf.TextureBytes(sz[0], sz[1])
		p := &pipeline{slides: pdf.NewCache(autoBudget(sz[0], sz[1]))}
		ahead := p.prefetchAhead(page)
		if need := (2 + ahead) * page; need > p.slides.Budget() {
			t.Errorf("%dx%d: working set %d MB (ahead=%d) exceeds budget %d MB",
				sz[0], sz[1], need>>20, ahead, p.slides.Budget()>>20)
		}
		if ahead < 1 {
			t.Errorf("%dx%d: ahead = %d, want at least 1", sz[0], sz[1], ahead)
		}
	}
}

func TestPresenterBudgetCoversPanes(t *testing.T) {
	lo := (&PresenterGame{bufW: 3840, bufH: 2160}).presenterLayout()
	cur := pdf.TextureBytes(lo.currentContent.w, lo.currentContent.h)
	next := pdf.TextureBytes(lo.nextContent.w, lo.nextContent.h)
	if got := presenterBudget(lo); got < 2*cur+2*next {
		t.Fatalf("presenterBudget = %d MB, want room for current+next panes and their prefetch (%d MB)", got>>20, (2*cur+2*next)>>20)
	}
}
