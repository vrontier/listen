module github.com/vrontier/listen/listener

go 1.23

require (
	github.com/coder/websocket v1.8.15
	github.com/vrontier/listen/ingest v0.0.0
)

replace github.com/vrontier/listen/ingest => ../ingest
