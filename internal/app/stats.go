package app

import (
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
)

// statsReporter prints memory and render diagnostics to stderr every two
// seconds when --debug-stats is set. A nil reporter does nothing.
type statsReporter struct {
	role string
	next time.Time
}

func (s *statsReporter) tick(p *pipeline, listIdx int) {
	if s == nil || p == nil {
		return
	}
	now := time.Now()
	if now.Before(s.next) {
		return
	}
	s.next = now.Add(2 * time.Second)

	var di ebiten.DebugInfo
	ebiten.ReadDebugInfo(&di)
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	u, v, pf, bg := p.sched.QueueLens()
	renders, mean := p.sched.RenderStats()
	fmt.Fprintf(os.Stderr,
		"[boozle %s] slide=%d gpu=%s heap=%s sys=%s slides=%d/%s thumbs=%d/%s queue=u%d v%d p%d b%d renders=%d avg=%v\n",
		s.role, listIdx+1, mb(di.TotalGPUImageMemoryUsageInBytes), mb(int64(ms.HeapInuse)), mb(int64(ms.Sys)),
		p.slides.Len(), mb(int64(p.slides.Bytes())), p.thumbs.Len(), mb(int64(p.thumbs.Bytes())),
		u, v, pf, bg, renders, mean.Round(100*time.Microsecond))
}

func mb(b int64) string {
	return fmt.Sprintf("%.1fMB", float64(b)/(1<<20))
}
