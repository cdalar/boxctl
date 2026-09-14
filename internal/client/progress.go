package client

import (
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/term"
)

// showProgress gates all progress output on stderr actually being a
// terminal -- a carriage-return-driven progress line is meaningless (and
// messy) once piped to a file or another program, e.g. in a script.
var showProgress = term.IsTerminal(int(os.Stderr.Fd()))

// progressInterval throttles how often a progress line is reprinted --
// otherwise a fast local transfer would repaint the terminal on every
// single Read/Write call.
const progressInterval = 200 * time.Millisecond

// progressReader wraps r, printing running upload progress to stderr as
// it's read -- used by Import, where the total size (the local file's
// own stat) is always known upfront, so this always shows a percentage.
type progressReader struct {
	r         io.Reader
	total     int64
	read      int64
	lastPrint time.Time
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.read += int64(n)
	if time.Since(p.lastPrint) >= progressInterval {
		printProgress(p.read, p.total)
		p.lastPrint = time.Now()
	}
	return n, err
}

// progressWriter is progressReader's sibling for Download. total is 0
// when the server didn't report a Content-Length (a "full" backup has
// no knowable size at all -- see boxctl-vms's vmbundle.SizedBundle),
// in which case this prints a running byte count instead of a percentage.
type progressWriter struct {
	w         io.Writer
	total     int64
	written   int64
	lastPrint time.Time
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.written += int64(n)
	if time.Since(p.lastPrint) >= progressInterval {
		printProgress(p.written, p.total)
		p.lastPrint = time.Now()
	}
	return n, err
}

func printProgress(done, total int64) {
	if !showProgress {
		return
	}
	if total > 0 {
		pct := float64(done) / float64(total) * 100
		fmt.Fprintf(os.Stderr, "\r%s / %s (%.0f%%)", formatBytes(done), formatBytes(total), pct)
	} else {
		fmt.Fprintf(os.Stderr, "\r%s", formatBytes(done))
	}
}

// finishProgress prints one last update and ends the line -- called once
// the transfer is over (successfully or not: a partial byte count on
// failure is still accurate information, and this keeps whatever error
// message follows from landing on the same line as the progress bar).
func finishProgress(done, total int64) {
	if !showProgress {
		return
	}
	printProgress(done, total)
	fmt.Fprintln(os.Stderr)
}

// waitFrames animates printWaiting's spinner -- there's no byte count or
// percentage to show while polling boxctl-vms's async backup/import
// status (the backend doesn't report incremental progress during its
// own build/upload or fetch/reconstruct step), so this is purely a
// "still running, not stuck" signal alongside elapsed time.
var waitFrames = [...]rune{'|', '/', '-', '\\'}

// printWaiting prints a one-line, carriage-return-updated spinner plus
// elapsed time, gated by the same showProgress terminal check as
// printProgress/finishProgress -- used by pollTransfer while it waits on
// boxctl-vms's agent to finish building+uploading a bundle to R2
// (backup) or fetching+reconstructing one from it (import), since both
// can take anywhere from seconds to a couple minutes with nothing else
// to show for it in the meantime.
func printWaiting(label string, tick int, elapsed time.Duration) {
	if !showProgress {
		return
	}
	frame := waitFrames[tick%len(waitFrames)]
	fmt.Fprintf(os.Stderr, "\r%c %s (%s)", frame, label, elapsed.Round(time.Second))
}

// finishWaiting clears printWaiting's line -- called once pollTransfer's
// wait is over (successfully or not), same reasoning as finishProgress:
// whatever message follows shouldn't land on the same line as a
// half-drawn spinner.
func finishWaiting() {
	if !showProgress {
		return
	}
	fmt.Fprint(os.Stderr, "\r\033[K")
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
