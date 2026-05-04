package app

import (
	"reflect"
	"testing"
	"time"

	"github.com/gethash/boozle/internal/config"
	"github.com/gethash/boozle/internal/ipc"
	"github.com/gethash/boozle/internal/timer"
)

func TestBuildPageList(t *testing.T) {
	all := buildPageList(4, config.PageRange{All: true})
	if !reflect.DeepEqual(all, []int{0, 1, 2, 3}) {
		t.Fatalf("all pages = %v, want [0 1 2 3]", all)
	}

	filtered := buildPageList(5, config.PageRange{Pages: []int{2, 4, 9}})
	if !reflect.DeepEqual(filtered, []int{1, 3}) {
		t.Fatalf("filtered pages = %v, want [1 3]", filtered)
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
		cfg:       config.Config{Notes: map[int]string{5: "Presenter note"}},
		auto:      a,
		pageList:  []int{2, 4, 6},
		listIdx:   1,
		startedAt: time.Now().Add(-90 * time.Second),
		stateCh:   make(chan ipc.PresenterState, 1),
	}

	g.broadcastState()
	st := <-g.stateCh
	if st.Page != 4 || st.ListIndex != 1 || st.Total != 3 || st.NextPage != 6 {
		t.Fatalf("state page metadata = %+v, want page=4 index=1 total=3 next=6", st)
	}
	if st.ElapsedSeconds < 89 || st.ElapsedSeconds > 91 {
		t.Fatalf("ElapsedSeconds = %d, want about 90", st.ElapsedSeconds)
	}
	if st.Notes != "Presenter note" {
		t.Fatalf("Notes = %q, want presenter note", st.Notes)
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
	gridX, gridY, cols, rows, cellW, cellH, _ := computeOvGrid(12, 1920, 1080)
	if gridX != ov.gridContent.x || gridY != ov.gridContent.y {
		t.Fatalf("grid origin = %d,%d, want %d,%d", gridX, gridY, ov.gridContent.x, ov.gridContent.y)
	}
	if cols <= 0 || rows <= 0 || cellW <= 0 || cellH <= 0 {
		t.Fatalf("invalid grid cols=%d rows=%d cell=%fx%f", cols, rows, cellW, cellH)
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
