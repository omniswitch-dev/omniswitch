package gateway

// size reports the number of tracked rate-limit windows.
func (backend *localRateLimitBackend) size() int {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return len(backend.windows)
}

// size reports the number of ledger entries.
func (l *budgetLedger) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}
