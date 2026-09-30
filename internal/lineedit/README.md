# lineedit

Prompt input with cursor movement, in-memory history, and wrapping for long
lines. It's a thin wrapper around `golang.org/x/term`. When stdin
isn't a terminal, it falls back to plain line reading.

```go
rl := lineedit.New(os.Stdin, os.Stdout, "> ")
for {
    line, err := rl.ReadLine()
    if errors.Is(err, lineedit.ErrInterrupt) { continue } // Ctrl-C: line discarded
    if err != nil { break }                               // io.EOF: Ctrl-D
    // ...
}
```

## Usage notes

- **Leave the cursor at column 0 before calling `ReadLine`.** The editor
  assumes it starts on a fresh line. End any output you've printed with a
  newline.
- **The terminal is in raw mode only while `ReadLine` runs.** Output between
  calls behaves normally, and Ctrl-C then sends SIGINT. Don't call
  `term.MakeRaw` yourself.
- **Ctrl-C at the prompt is a key, not a signal.** It returns `ErrInterrupt`,
  and the cancelled line isn't added to history. Ctrl-D on an empty line
  returns `io.EOF`.
- **Use one reader at a time.** A `Reader` isn't safe for concurrent use.
  Don't read stdin anywhere else while it exists, because it may hold input
  that has already been read.
- **History is in memory only.** It holds up to 100 lines and is lost when the
  process exits. If you save it to a file, use mode `0600`, because prompts
  can contain secrets.
- **Multi-line paste isn't supported.** Each pasted newline submits a line.
