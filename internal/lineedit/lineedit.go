// Package lineedit reads prompt input with cursor movement, history, and
// wrapping for long lines. It wraps golang.org/x/term's line editor and falls
// back to plain line reading when stdin is not a terminal.
package lineedit

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"

	"golang.org/x/term"
)

// ErrInterrupt is returned by ReadLine when the user presses Ctrl-C.
var ErrInterrupt = errors.New("interrupt")

// maxHistory bounds the in-memory history.
const maxHistory = 100

const keyCtrlC = 3

// interruptSeq replaces a Ctrl-C byte before x/term sees it: Ctrl-E moves the
// cursor to the end of the line, then Enter finishes it. x/term treats a raw
// Ctrl-C the same as Ctrl-D (returns io.EOF) and leaves the partial line in its
// buffer, so we turn it into a normal line ending and mark it as interrupted.
var interruptSeq = []byte{5, '\r'}

type Reader struct {
	// Terminal mode; t is nil when stdin is not a terminal.
	t    *term.Terminal
	in   *interruptReader
	fd   int
	hist *history

	// Fallback mode.
	scan   *bufio.Scanner
	out    io.Writer
	prompt string
}

// New returns a line editor if in is a terminal, otherwise a plain line
// scanner so piped input keeps working.
func New(in *os.File, out io.Writer, prompt string) *Reader {
	fd := int(in.Fd())
	if !term.IsTerminal(fd) {
		scan := bufio.NewScanner(in)
		scan.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		return &Reader{fd: -1, scan: scan, out: out, prompt: prompt}
	}
	r := newTerminalReader(in, out, prompt)
	r.fd = fd
	return r
}

// newTerminalReader builds the editor over any reader/writer without touching
// terminal modes, so tests can drive it with raw bytes.
func newTerminalReader(in io.Reader, out io.Writer, prompt string) *Reader {
	ir := &interruptReader{r: in}
	t := term.NewTerminal(struct {
		io.Reader
		io.Writer
	}{ir, out}, prompt)
	h := &history{in: ir}
	t.History = h
	return &Reader{t: t, in: ir, fd: -1, hist: h}
}

// ReadLine returns the next line, ErrInterrupt on Ctrl-C, or io.EOF on Ctrl-D
// at an empty prompt.
//
// The terminal is in raw mode only for the duration of the call, so output
// written between calls behaves normally (newlines, Ctrl-C as SIGINT). The
// caller must leave the cursor at the start of a line before calling.
func (r *Reader) ReadLine() (string, error) {
	if r.t == nil {
		io.WriteString(r.out, r.prompt)
		if !r.scan.Scan() {
			if err := r.scan.Err(); err != nil {
				return "", err
			}
			return "", io.EOF
		}
		return r.scan.Text(), nil
	}

	if r.fd >= 0 {
		// Some ptys report 0x0; x/term would then wrap after every character.
		if w, h, err := term.GetSize(r.fd); err == nil && w > 0 {
			r.t.SetSize(w, h)
		}
		state, err := term.MakeRaw(r.fd)
		if err != nil {
			return "", err
		}
		defer term.Restore(r.fd, state)
	}

	r.in.interrupted = false
	line, err := r.t.ReadLine()
	if r.in.interrupted {
		return "", ErrInterrupt
	}
	if errors.Is(err, term.ErrPasteIndicator) {
		// Only returned with bracketed paste enabled; the line is still valid.
		err = nil
	}
	return line, err
}

// interruptReader swaps each Ctrl-C byte for interruptSeq and records that it
// did. The swapped sequence is always handed out in a Read of its own, so the
// ReadLine that consumes it returns exactly at its Enter, and no earlier line
// can be mistaken for the interrupted one.
type interruptReader struct {
	r           io.Reader
	buf         []byte // read from r but not yet handed out
	inject      []byte // rest of interruptSeq still to hand out
	interrupted bool
}

func (ir *interruptReader) Read(p []byte) (int, error) {
	if len(ir.inject) > 0 {
		n := copy(p, ir.inject)
		ir.inject = ir.inject[n:]
		return n, nil
	}
	if len(ir.buf) == 0 {
		tmp := make([]byte, len(p))
		n, err := ir.r.Read(tmp)
		if n == 0 {
			return 0, err
		}
		// Drop err when n > 0: the next Read of r will report it again.
		ir.buf = tmp[:n]
	}
	switch i := bytes.IndexByte(ir.buf, keyCtrlC); {
	case i == 0:
		ir.buf = ir.buf[1:]
		ir.interrupted = true
		ir.inject = interruptSeq
		return ir.Read(p)
	case i > 0:
		n := copy(p, ir.buf[:i])
		ir.buf = ir.buf[n:]
		return n, nil
	default:
		n := copy(p, ir.buf)
		ir.buf = ir.buf[n:]
		return n, nil
	}
}

// history is a bounded term.History that skips blank lines, repeats of the
// previous entry, and lines cancelled with Ctrl-C.
type history struct {
	in      *interruptReader
	entries []string // oldest first
}

func (h *history) Add(entry string) {
	if entry == "" || h.in.interrupted {
		return
	}
	if n := len(h.entries); n > 0 && h.entries[n-1] == entry {
		return
	}
	if len(h.entries) == maxHistory {
		h.entries = h.entries[1:]
	}
	h.entries = append(h.entries, entry)
}

func (h *history) Len() int { return len(h.entries) }

func (h *history) At(idx int) string { return h.entries[len(h.entries)-1-idx] }
