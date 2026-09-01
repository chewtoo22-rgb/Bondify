package mobile

import "fmt"

// runtimePathRegistry owns the Android runtime mapping between stable path labels and the
// protocol's uint8 path IDs. A reservation spans fd adoption plus PATH_ADD so concurrent
// NetworkCallback activity cannot register the same label twice or reuse an in-flight ID.
type runtimePathRegistry struct {
	labelToID     map[string]uint8
	pendingLabels map[string]uint8
	pendingIDs    map[uint8]struct{}
	nextID        uint8
}

func newRuntimePathRegistry(initialLabels []string) runtimePathRegistry {
	r := runtimePathRegistry{
		labelToID:     make(map[string]uint8, len(initialLabels)),
		pendingLabels: make(map[string]uint8),
		pendingIDs:    make(map[uint8]struct{}),
		nextID:        uint8(len(initialLabels)),
	}
	for i, label := range initialLabels {
		r.labelToID[label] = uint8(i)
	}
	return r
}

// reserve claims both label and an unused protocol path ID. It scans the entire uint8 ID
// space from nextID, so long-running sessions survive counter wrap as long as at least one
// ID is actually free.
func (r *runtimePathRegistry) reserve(label string) (uint8, error) {
	if _, exists := r.labelToID[label]; exists {
		return 0, fmt.Errorf("mobile: path %q already registered; call DropPathLabel first", label)
	}
	if _, exists := r.pendingLabels[label]; exists {
		return 0, fmt.Errorf("mobile: path %q is already being registered", label)
	}

	for scanned := 0; scanned < 256; scanned++ {
		id := r.nextID
		r.nextID++
		if r.idInUse(id) {
			continue
		}
		r.pendingLabels[label] = id
		r.pendingIDs[id] = struct{}{}
		return id, nil
	}
	return 0, fmt.Errorf("mobile: no runtime path IDs available")
}

func (r *runtimePathRegistry) idInUse(id uint8) bool {
	if _, exists := r.pendingIDs[id]; exists {
		return true
	}
	for _, activeID := range r.labelToID {
		if activeID == id {
			return true
		}
	}
	return false
}

func (r *runtimePathRegistry) release(label string, id uint8) {
	if pendingID, ok := r.pendingLabels[label]; ok && pendingID == id {
		delete(r.pendingLabels, label)
		delete(r.pendingIDs, id)
	}
}

func (r *runtimePathRegistry) commit(label string, id uint8) {
	r.release(label, id)
	r.labelToID[label] = id
}

func (r *runtimePathRegistry) lookup(label string) (uint8, bool) {
	id, ok := r.labelToID[label]
	return id, ok
}
