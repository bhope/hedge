# Contributing to hedge

Contributions are welcome. Please open an issue to discuss your idea before submitting a PR so we can brainstorm and align on scope and approach.

## Running tests

```
go test -race ./...
```

## Running benchmarks

Microbenchmarks (transport overhead, DDSketch, token bucket):

```
go test ./benchmark/ -run=^$ -bench=. -benchmem
```

End-to-end simulation (50k requests, four configurations):

```
cd benchmark/simulate && go run .
```

## Generating the evaluation chart

```
cd benchmark/chart && go run .
```

## Code style

All submissions must pass:

```
go vet ./...
gofmt -l .
```

No new external dependencies in the main module without prior discussion.

## Pull requests

- Keep PRs focused - one concern per PR
- Include tests for new behaviour
- Update the README if public API or configuration changes
