package reorder

// Stop releases timer/heap resources owned by this buffer. It intentionally does not close
// Out(): a caller may still have a receiver goroutine selecting on that channel while the
// owning session is being retired. The receiver should use its own lifecycle signal to exit.
// Stop is safe to call more than once.
func (b *Buffer) Stop() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	b.h = nil
	b.inHeap = make(map[uint64]struct{})
	b.curBytes = 0
	b.headSet = false
}
