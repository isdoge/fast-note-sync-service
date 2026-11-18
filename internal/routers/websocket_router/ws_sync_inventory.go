package websocket_router

// syncInventory 将有路径项与旧客户端的无路径项分开保存。
// 无路径项按哈希排队，避免空路径或重复哈希覆盖其他清单项。
type syncInventory[T any] struct {
	byPath map[string]T
	byHash map[string][]T
}

func newSyncInventory[T any]() *syncInventory[T] {
	return &syncInventory[T]{byPath: make(map[string]T), byHash: make(map[string][]T)}
}

func (s *syncInventory[T]) add(path, hash string, item T) {
	if path != "" {
		s.byPath[path] = item
	} else {
		s.byHash[hash] = append(s.byHash[hash], item)
	}
}

// take 优先按路径匹配，再退回无路径项的哈希；每项最多消费一次。
func (s *syncInventory[T]) take(path, hash string) (T, bool) {
	if item, ok := s.byPath[path]; ok {
		delete(s.byPath, path)
		return item, true
	}
	if items := s.byHash[hash]; len(items) > 0 {
		item := items[0]
		if len(items) == 1 {
			delete(s.byHash, hash)
		} else {
			s.byHash[hash] = items[1:]
		}
		return item, true
	}
	var zero T
	return zero, false
}

func (s *syncInventory[T]) remaining() []T {
	items := make([]T, 0, len(s.byPath)+len(s.byHash))
	for _, item := range s.byPath {
		items = append(items, item)
	}
	for _, queue := range s.byHash {
		items = append(items, queue...)
	}
	return items
}
