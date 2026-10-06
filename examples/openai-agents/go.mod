module github.com/cdalar/boxctl/examples/openai-agents

go 1.26.5

require (
	github.com/cdalar/boxctl v0.0.0
	github.com/gorilla/websocket v1.5.3
	github.com/openai/openai-go/v3 v3.68.0
)

require (
	github.com/coder/websocket v1.8.15 // indirect
	github.com/tidwall/gjson v1.19.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/term v0.46.0 // indirect
)

replace github.com/cdalar/boxctl => ../..
