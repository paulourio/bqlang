package formatter

func allMatch[T any](items []T, pred func(T) bool) bool {
	for _, item := range items {
		if !pred(item) {
			return false
		}
	}

	return true
}
