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

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/gethash/boozle/internal/pdf"
)

const overviewThumbCacheBytes = 128 << 20

type overviewThumbStore struct {
	gpu  *pdf.Cache
	disk *overviewDiskThumbCache
}

func newOverviewThumbStore(pdfPath string) *overviewThumbStore {
	return &overviewThumbStore{
		gpu:  pdf.NewCache(overviewThumbCacheBytes),
		disk: newOverviewDiskThumbCache(pdfPath),
	}
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

func (s *overviewThumbStore) Put(key pdf.CacheKey, img *ebiten.Image) {
	if s != nil && s.gpu != nil && img != nil {
		s.gpu.Put(key, img)
	}
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
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d", abs, info.Size(), info.ModTime().UnixNano())))
	return &overviewDiskThumbCache{
		root: filepath.Join(os.TempDir(), "boozle-thumbs"),
		base: hex.EncodeToString(sum[:]),
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
	f, err := os.Open(c.path(key))
	if err != nil {
		return nil, false
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return nil, false
	}
	return toRGBA(img), true
}

func (c *overviewDiskThumbCache) Save(key pdf.CacheKey, img image.Image) {
	if c == nil {
		return
	}
	if err := os.MkdirAll(c.root, 0o755); err != nil {
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
	if err := png.Encode(tmp, img); err != nil {
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
