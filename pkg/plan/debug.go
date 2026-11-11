package plan

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Debug output (--debug): what the CLI does ([step]) and what Helm does
// ([helm]), on one clock, to stderr. Off by default.
var (
	debugMu    sync.Mutex
	debugW     io.Writer
	debugStart time.Time
)

// SetDebug turns debug output on, writing to w; nil turns it off.
func SetDebug(w io.Writer) {
	debugMu.Lock()
	defer debugMu.Unlock()
	debugW, debugStart = w, time.Now()
}

// Debugging reports whether --debug is on.
func Debugging() bool {
	debugMu.Lock()
	defer debugMu.Unlock()
	return debugW != nil
}

func debugLine(kind, format string, args ...interface{}) {
	debugMu.Lock()
	defer debugMu.Unlock()
	if debugW == nil {
		return
	}
	msg := strings.TrimRight(fmt.Sprintf(format, args...), "\n")
	fmt.Fprintf(debugW, "%s %s\n", Dim(fmt.Sprintf("[%-4s %6.1fs]", kind, time.Since(debugStart).Seconds())), msg)
}

// stepf logs a CLI step.
func stepf(format string, args ...interface{}) { debugLine("step", format, args...) }

// HelmLog is Helm's logger under --debug; nil (silent) otherwise.
func HelmLog() func(string, ...interface{}) {
	if !Debugging() {
		return nil
	}
	return func(format string, args ...interface{}) { debugLine("helm", format, args...) }
}
