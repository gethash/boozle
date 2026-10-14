package pdf

import "sync"

// pixPools recycles RGBA pixel buffers, keyed by exact byte length. Slides
// in one deck are rendered at a handful of recurring sizes, so exact-size
// buckets get near-perfect reuse without the 2× waste of power-of-two
// classes. sync.Pool drops idle buffers at GC, so the map cannot pin memory.
var pixPools sync.Map // int -> *sync.Pool

func getPix(n int) []byte {
	if p, ok := pixPools.Load(n); ok {
		if b, ok := p.(*sync.Pool).Get().(*[]byte); ok {
			return *b
		}
	}
	return make([]byte, n)
}

func putPix(b []byte) {
	if len(b) == 0 {
		return
	}
	p, _ := pixPools.LoadOrStore(len(b), &sync.Pool{})
	p.(*sync.Pool).Put(&b)
}
