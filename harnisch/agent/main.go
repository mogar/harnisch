// Harnisch agent is a minimal LLM agent harness.
//
// example usage: agent -model qwen3.5:35b -root ~/code/project
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mogar/harnisch/internal/agent"
	"github.com/mogar/harnisch/internal/provider/ollama"
	"github.com/mogar/harnisch/internal/tool"
)

const defaultSystemPrompt = `You are a coding assistant operating inside a workspace directory.

Use the provided tools to inspect files before answering questions about them.
Do not guess at file contents. If a tool returns an error, read the error and
adjust rather than repeating the same call.

Answer concisely.`

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error: ", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		host = flag.String("host", "http://localhost:11434", "Ollama server URL")
		model = flag.String("model", "qwen3.5:35b", "model name")
		rootDir = flag.String("root", ".", "workspace root; tools cannot escape it")
	)
	flag.Parse()

	root, err := tool.NewRoot(*rootDir)
	if err != nil {
		return fmt.Errorf("workspace root: %w", err)
	}

	registry := tool.NewRegistry()
	for _, t := range []tool.Tool {
		tool.ReadFile{Root: root},
		tool.ListDir{Root: root},
	} {
		if err := registry.Register(t); err != nil {
			return err
		}
	}

	a := &agent.Agent{
		Provider: ollama.New(*host, *model),
		Tools: registry,
		System: defaultSystemPrompt,
	}

	fmt.Printf("model: %s workspace %s\n", *model, root.Dir())
	fmt.Println("Ctrl-C cancels the current turn; Ctrl-D or /quit exits.")

	// Signal handling: SIGINT cancels the turn
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for {
		fmt.Print("\n> ")
		if !in.Scan() { // Ctrl-D
			fmt.Println()
			break
		}
		line := strings.TrimSpace(in.Text())
		if line == "" {
			continue
		}
		if line == "/quit" || line == "/exit" {
			// TODO: dictionary of commands instead of hard coding
			break
		}
		if line == "/usage" {
			fmt.Printf("in=%d out=%d cache_read=%d cache_write=%d\n",
				a.Usage.InputTokens, a.Usage.OutputTokens,
				a.Usage.CacheReadTokens, a.Usage.CacheWriteTokens)
			continue
		}

		// Fresh context on each turn
		turnCtx, cancel := context.WithCancel(context.Background())

		// Watcher to convert signal into a context cancellation
		done := make(chan struct{})
		go func() {
			select {
			case <-sigCh:
				fmt.Print("\n[interrupted]\n")
				cancel()
			case <-done:
			}
		}()

		err := a.Turn(turnCtx, line, os.Stdout)
		close(done)
		cancel() // always call cancel to avoid context leaks

		fmt.Println()
		if err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "turn failed: ", err)
		}
	}
	return in.Err()
}