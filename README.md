# Octoryn Go SDK

Governed model access for Go 1.23+.

```bash
go get github.com/octoryn/octoryn-go@v0.1.1
```

```go
client, err := octoryn.NewClient(os.Getenv("OCTORYN_API_KEY"))
result, err := client.GenerateText(ctx, octoryn.GenerateTextParams{
    Model:  "policy/au-enterprise",
    Prompt: "Explain this routing decision.",
})
fmt.Println(result.Text, result.Octoryn.EvidenceHash)
```

`StreamText` exposes replayable iterators, typed tool-call payloads, usage and
governance metadata. `GenerateObject` sends strict JSON Schema and decodes the
validated result into a Go type.
