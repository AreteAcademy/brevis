package agent

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"time"

	"github.com/AreteAcademy/brevis/internal/execution/remote"
)

// The stream half: a bounded ring of lines that outlives any one connection.
//
// This is what makes a dropped connection resumable. The process writes into
// the ring whether anybody is reading or not, and a reader is a cursor over it
// -- so two readers, or none, or one that disappears and comes back, all work
// without the process noticing.

// emit appends one line, numbering it, and wakes every reader.
//
// The sequence is assigned HERE and nowhere else, which is what makes it
// monotonic and never reused. The engine's deduplication depends on that: a
// replayed line with a fresh number would be counted twice, and a counter
// metric inside it would be added twice.
func (e *execution) emit(l remote.Line) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	l.Seq = e.next
	e.next++
	e.lines = append(e.lines, l)

	// Trim from the front, and move `first` with it. That variable is the whole
	// gap answer: it says the oldest line still available, which is what a
	// resume is measured against.
	if len(e.lines) > e.ring {
		drop := len(e.lines) - e.ring
		e.lines = e.lines[drop:]
		e.first += int64(drop)
	}
	e.cond.Broadcast()
}

// close marks the stream finished and releases every reader.
func (e *execution) close() {
	e.mu.Lock()
	e.closed = true
	e.mu.Unlock()
	e.cond.Broadcast()
}

// copy turns one pipe into lines.
//
// One goroutine per stream, which means the order BETWEEN stdout and stderr is
// not preserved -- and cannot be. They are separate pipes with separate
// buffers, and nothing below this promises that a line written to one appears
// before a line written later to the other.
//
// Within a stream the order is exact, which is what matters: a stack trace
// arrives in one piece. The local executor forwards them the same way for the
// same reason, and the pod executor cannot tell them apart at all because
// Kubernetes merges them. Keeping the two apart is worth more than an
// interleaving nobody can guarantee -- it is what stopped dbt's "Parsing
// Error", printed on stdout, from being read as ordinary output.
func (e *execution) copy(r io.Reader, stream string) {
	s := bufio.NewScanner(r)
	// The same ceiling the engine's executors use, and for the same reason: a
	// dbt line carrying SQL exceeds the scanner's 64 KB default, and a Scanner
	// that overflows stops reading in silence.
	s.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for s.Scan() {
		e.emit(remote.Line{Kind: remote.KindLog, Stream: stream, Message: s.Text()})
	}
}

// beat renews the lease while the step runs.
//
// A step can legitimately print nothing for an hour, so silence cannot mean
// death. This is what tells the two apart, and it stops the moment the process
// does -- an agent that went on beating for a finished step would be reporting
// a lease it no longer holds.
func (e *execution) beat(every time.Duration, done <-chan struct{}) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			e.emit(remote.Line{Kind: remote.KindAlive})
		}
	}
}

// reader serves the stream from `after`, as NDJSON.
//
// A pipe rather than a buffer, because the whole point is that it streams: a
// step running for forty minutes must reach the engine as it goes, not when it
// ends.
func (e *execution) reader(after int64) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		enc := json.NewEncoder(pw)
		cursor := after

		for {
			e.mu.Lock()
			for !e.closed && (len(e.lines) == 0 || e.lines[len(e.lines)-1].Seq <= cursor) {
				e.cond.Wait()
			}
			// Everything after the cursor that is still in the ring. A reader
			// that fell behind the trim resumes from the oldest available
			// rather than from nothing -- the engine is what decides a gap is
			// fatal, and it does so from `first` at resume time.
			var batch []remote.Line
			for _, l := range e.lines {
				if l.Seq > cursor {
					batch = append(batch, l)
				}
			}
			finished := e.closed && len(batch) == 0
			e.mu.Unlock()

			if finished {
				_ = pw.Close()
				return
			}
			for _, l := range batch {
				if err := enc.Encode(l); err != nil {
					// The engine hung up. The process goes on, the ring goes on
					// filling, and a resume picks it up -- which is the reason
					// the lines do not live on the connection.
					_ = pw.CloseWithError(err)
					return
				}
				cursor = l.Seq
			}
		}
	}()

	// Closing the reader must release the writer, or a disconnected engine
	// leaves this goroutine blocked on a pipe forever.
	return &readerClose{PipeReader: pr, writer: pw}
}

type readerClose struct {
	*io.PipeReader
	writer *io.PipeWriter
}

func (r *readerClose) Close() error {
	_ = r.writer.CloseWithError(io.ErrClosedPipe)
	return r.PipeReader.Close()
}

// asExit is errors.As with the concrete type, kept here so agent.go reads
// without the ceremony.
func asExit(err error, target **exec.ExitError) bool { return errors.As(err, target) }
