# Harnisch - A safe LLM harness

## TODO

* hitting ^C puts it in the chat, and you have to hit enter to interrupt
  * should just send the signal immediately to the chat without enter
* no text response from model
* backspace

## Build and run

```sh
go mod tidy
go vet ./...         # static checks; run this constantly
go test ./...        # replay tests, no Ollama server needed
go build -o bin/agent ./harnisch/agent

./bin/agent -model qwen3.5:35b -root ~/some/project
```

`go run ./harnisch/agent` compiles and runs in one step for quick iteration.

## Models Providers Supported

### ollama

Start ollama and verify server functionality with the following command. Note that you can change models to suit, but not all models support tool calling. 

```sh
curl http://localhost:11434/api/chat -d '{
  "model": "qwen3.5:35b",
  "messages": [{"role":"user","content":"List the files in the current directory."}],
  "stream": false,
  "tools": [{"type":"function","function":{
    "name":"list_dir",
    "description":"List directory entries",
    "parameters":{"type":"object","properties":{"path":{"type":"string"}}}
  }}]
}' | jq '.message'
```

## Contributing

1. create a new PR
2. make sure your code is unit-tested
3. `gofmt -w .` before committing