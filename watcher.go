package main

// repeatWatcher tracks recent lines and tells you when one repeats too often.
type repeatWatcher struct {
	windowSize      int
	repeatThreshold int
	recentLines     []string
	lineCounts      map[string]int
}

// newRepeatWatcher creates a watcher ready to use.
func newRepeatWatcher(windowSize int, repeatThreshold int) *repeatWatcher {
	return &repeatWatcher{
		windowSize:      windowSize,
		repeatThreshold: repeatThreshold,
		recentLines:     make([]string, 0, windowSize),
		lineCounts:      make(map[string]int),
	}
}

// observe records a new line and returns true if it has now repeated
// enough times to trip the breaker.
func (w *repeatWatcher) observe(line string) bool {
	if line == "" {
		return false
	}

	w.recentLines = append(w.recentLines, line)
	w.lineCounts[line]++

	if len(w.recentLines) > w.windowSize {
		oldest := w.recentLines[0]
		w.recentLines = w.recentLines[1:]
		w.lineCounts[oldest]--
		if w.lineCounts[oldest] <= 0 {
			delete(w.lineCounts, oldest)
		}
	}

	return w.lineCounts[line] >= w.repeatThreshold
}

// countFor returns how many times the given line currently appears
// in the window — used for the alert message.
func (w *repeatWatcher) countFor(line string) int {
	return w.lineCounts[line]
}
