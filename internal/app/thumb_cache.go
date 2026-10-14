package app

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/gethash/boozle/internal/cachedir"
	"github.com/gethash/boozle/internal/pdf"
)

// overviewThumbCacheMin is the smallest thumbnail GPU budget; the actual
// budget follows the grid (see fitTo).
const overviewThumbCacheMin = 8 << 20

type overviewThumbStore struct {
	gpu  *pdf.Cache
	disk *overviewDiskThumbCache
}

func newOverviewThumbStore(pdfPath string) *overviewThumbStore {
	return &overviewThumbStore{
		gpu:  pdf.NewCache(overviewThumbCacheMin),
		disk: newOverviewDiskThumbCache(pdfPath),
	}
}

// fitTo prepares the store for an overview of pages thumbnails at w×h:
// thumbnails of other sizes (from an earlier window size) are dropped and
// the budget is set to twice what the grid needs, so reopening the
// overview is instant without holding a fixed large reserve.
func (s *overviewThumbStore) fitTo(w, h, pages int) {
	if s == nil || s.gpu == nil {
		return
	}
	s.gpu.PurgeExcept(func(k pdf.CacheKey) bool { return k.W == w && k.H == h })
	s.gpu.Resize(max(overviewThumbCacheMin, 2*pages*pdf.TextureBytes(w, h)))
}

func (s *overviewThumbStore) Clear() {
	if s != nil && s.gpu != nil {
		s.gpu.Clear()
	}
}

func (s *overviewThumbStore) Get(key pdf.CacheKey) (*ebiten.Image, bool) {
	if s == nil || s.gpu == nil {
		return nil, false
	}
	img, ok := s.gpu.Get(key)
	if !ok {
		return nil, false
	}
	return img.(*ebiten.Image), true
}

func (s *overviewThumbStore) Len() int {
	if s == nil || s.gpu == nil {
		return 0
	}
	return s.gpu.Len()
}

func (s *overviewThumbStore) Bytes() int {
	if s == nil || s.gpu == nil {
		return 0
	}
	return s.gpu.Bytes()
}

func (s *overviewThumbStore) Has(key pdf.CacheKey) bool {
	return s != nil && s.gpu != nil && s.gpu.Has(key)
}

// Put caches img and returns the image now cached at key (an existing
// one wins over img).
func (s *overviewThumbStore) Put(key pdf.CacheKey, img *ebiten.Image) *ebiten.Image {
	if s == nil || s.gpu == nil || img == nil {
		return img
	}
	return s.gpu.Put(key, img).(*ebiten.Image)
}

func (s *overviewThumbStore) Pin(key pdf.CacheKey) {
	if s != nil && s.gpu != nil {
		s.gpu.Pin(key)
	}
}

func (s *overviewThumbStore) Unpin(key pdf.CacheKey) {
	if s != nil && s.gpu != nil {
		s.gpu.Unpin(key)
	}
}

func (s *overviewThumbStore) LoadDisk(key pdf.CacheKey) (*image.RGBA, bool) {
	if s == nil || s.disk == nil {
		return nil, false
	}
	return s.disk.Load(key)
}

func (s *overviewThumbStore) SaveDisk(key pdf.CacheKey, img image.Image) {
	if s != nil && s.disk != nil && img != nil {
		s.disk.Save(key, img)
	}
}

const (
	// Thumbnails not opened for thumbMaxAge are pruned, and the oldest are
	// pruned while the directory exceeds thumbMaxBytes.
	thumbMaxAge   = 30 * 24 * time.Hour
	thumbMaxBytes = 200 << 20
)

var pruneThumbsOnce sync.Once

// overviewDiskThumbCache persists overview thumbnails as PNGs in the
// private per-user cache directory (<user cache>/boozle/thumbs, 0700),
// keyed by PDF path, size, modification time, page and thumbnail size.
type overviewDiskThumbCache struct {
	root string
	base string
}

func newOverviewDiskThumbCache(pdfPath string) *overviewDiskThumbCache {
	abs, err := filepath.Abs(pdfPath)
	if err != nil {
		abs = pdfPath
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil
	}
	root, err := cachedir.Dir("thumbs")
	if err != nil {
		return nil
	}
	pruneThumbsOnce.Do(func() {
		go pruneThumbDir(root, time.Now(), thumbMaxAge, thumbMaxBytes)
	})
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d", abs, info.Size(), info.ModTime().UnixNano())))
	return &overviewDiskThumbCache{
		root: root,
		base: hex.EncodeToString(sum[:]),
	}
}

// pruneThumbDir deletes thumbnails older than maxAge (by modification time,
// which Load refreshes), then the least recently used ones until the
// directory fits maxBytes.
func pruneThumbDir(root string, now time.Time, maxAge time.Duration, maxBytes int64) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	type file struct {
		path string
		size int64
		mod  time.Time
	}
	var files []file
	var total int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".png") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(root, e.Name())
		if now.Sub(info.ModTime()) > maxAge {
			_ = os.Remove(path)
			continue
		}
		files = append(files, file{path: path, size: info.Size(), mod: info.ModTime()})
		total += info.Size()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for _, f := range files {
		if total <= maxBytes {
			break
		}
		if os.Remove(f.path) == nil {
			total -= f.size
		}
	}
}

func (c *overviewDiskThumbCache) path(key pdf.CacheKey) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d|%d", c.base, key.Page, key.W, key.H)))
	return filepath.Join(c.root, hex.EncodeToString(sum[:])+".png")
}

func (c *overviewDiskThumbCache) Load(key pdf.CacheKey) (*image.RGBA, bool) {
	if c == nil {
		return nil, false
	}
	path := c.path(key)
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return nil, false
	}
	now := time.Now()
	_ = os.Chtimes(path, now, now) // mark as recently used for pruning
	return toRGBA(img), true
}

func (c *overviewDiskThumbCache) Save(key pdf.CacheKey, img image.Image) {
	if c == nil {
		return
	}
	tmp, err := os.CreateTemp(c.root, "thumb-*.png")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(tmp, img); err != nil {
		_ = tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	if err := os.Rename(tmpName, c.path(key)); err != nil {
		return
	}
	ok = true
}

func toRGBA(img image.Image) *image.RGBA {
	if rgba, ok := img.(*image.RGBA); ok {
		return rgba
	}
	b := img.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), img, b.Min, draw.Src)
	return rgba
}
