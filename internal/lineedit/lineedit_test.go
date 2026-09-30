package lineedit

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

type result struct {
	line string
	err  error
}

func (r result) String() string { return fmt.Sprintf("(%q, %v)", r.line, r.err) }

// readAll drives a terminal reader with raw key bytes until io.EOF.
func readAll(t *testing.T, input string) []result {
	t.Helper()
	r := newTerminalReader(strings.NewReader(input), io.Discard, "> ")
	var got []result
	for range 20 {
		line, err := r.ReadLine()
		got = append(got, result{line, err})
		if errors.Is(err, io.EOF) {
			return got
		}
	}
	t.Fatalf("no EOF after 20 reads: %v", got)
	return nil
}

func TestReadLine(t *testing.T) {
	eof := result{"", io.EOF}
	intr := result{"", ErrInterrupt}
	tests := []struct {
		name  string
		input string
		want  []result
	}{
		{"plain", "hello\r", []result{{"hello", nil}, eof}},
		{"left arrow insert", "helo\x1b[Dl\r", []result{{"hello", nil}, eof}},
		{"home and end", "ello\x1b[Hh\x1b[F!\r", []result{{"hello!", nil}, eof}},
		{"backspace", "abc\x7f\r", []result{{"ab", nil}, eof}},
		{"ctrl-u clears", "abc\x15xyz\r", []result{{"xyz", nil}, eof}},
		{"history up", "one\r\x1b[A\r", []result{{"one", nil}, {"one", nil}, eof}},
		{"ctrl-d on empty line", "\x04", []result{eof}},
		{"ctrl-d mid-line deletes", "ab\x1b[D\x04\r", []result{{"a", nil}, eof}},
		{"ctrl-c", "\x03", []result{intr, eof}},
		{"ctrl-c discards partial line", "abc\x03xyz\r", []result{intr, {"xyz", nil}, eof}},
		{"ctrl-c with cursor mid-line", "abc\x1b[D\x1b[D\x03\r", []result{intr, {"", nil}, eof}},
		{"line then ctrl-c in one read", "abc\r\x03", []result{{"abc", nil}, intr, eof}},
		{"empty line then ctrl-c", "\r\x03", []result{{"", nil}, intr, eof}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := readAll(t, tt.input)
			if fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHistorySkipsInterruptedAndBlank(t *testing.T) {
	// "one" is kept; the cancelled "two", the blank line and the repeated
	// "one" are not, so two presses of up land on "one" and stay there.
	got := readAll(t, "one\rtwo\x03\rone\r\x1b[A\x1b[A\r")
	want := []result{{"one", nil}, {"", ErrInterrupt}, {"", nil}, {"one", nil}, {"one", nil}, {"", io.EOF}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestHistoryBounded(t *testing.T) {
	h := &history{in: &interruptReader{}}
	for i := range maxHistory + 5 {
		h.Add(fmt.Sprint(i))
	}
	if h.Len() != maxHistory {
		t.Fatalf("Len = %d, want %d", h.Len(), maxHistory)
	}
	if got, want := h.At(0), fmt.Sprint(maxHistory+4); got != want {
		t.Errorf("At(0) = %q, want %q", got, want)
	}
	if got, want := h.At(maxHistory-1), "5"; got != want {
		t.Errorf("At(oldest) = %q, want %q", got, want)
	}
}

func TestFallbackWhenNotTerminal(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	go func() {
		pw.WriteString("a\nb\n")
		pw.Close()
	}()

	var out strings.Builder
	r := New(pr, &out, "> ")
	if r.t != nil {
		t.Fatal("expected fallback reader for a pipe")
	}
	var got []result
	for range 3 {
		line, err := r.ReadLine()
		got = append(got, result{line, err})
	}
	want := []result{{"a", nil}, {"b", nil}, {"", io.EOF}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if out.String() != "> > > " {
		t.Errorf("prompts = %q", out.String())
	}
}
