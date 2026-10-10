# Contributing

Thanks for taking a look. Issues and pull requests are both welcome.

## Getting set up

```sh
git clone https://github.com/kiraa06/claude-cl
cd claude-cl
go test ./...
go build -o ~/.local/bin/cl ./cmd/cl
```

Go 1.26 or newer. There are no dependencies beyond Bubble Tea and Lipgloss.

## Before opening a pull request

CI runs these on Linux and macOS, so run them first:

```sh
gofmt -l .          # must print nothing
go vet ./...
go test ./... -count=1
```

And check that every target still builds — `cl` ships for macOS and Linux, and
must at least compile for Windows:

```sh
for t in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64; do
  GOOS="${t%%/*}" GOARCH="${t##*/}" go build -o /dev/null ./cmd/cl || echo "broke $t"
done
```

## How the code is laid out

| Package | Responsibility |
|---|---|
| `internal/scan` | finding transcripts, extracting titles and metadata, search, trash |
| `internal/group` | splitting sessions into the HERE / REPO / ALL sections |
| `internal/launch` | building the `claude` invocation, model aliases, handing off |
| `internal/ui` | the Bubble Tea model and rendering |
| `cmd/cl` | flags and wiring |

Parsing, grouping, filtering and argv construction are pure functions, and are
tested as such. The picker is tested by feeding key messages to `Update` and
asserting on the resulting state — no golden screenshots.

## Testing against your own sessions

Two tests read your real `~/.claude/projects` and skip if it isn't there:

```sh
go test ./internal/scan -run TestLiveScan -v          # what cl would list, and how fast
go test ./internal/ui   -run TestLiveRender -v        # print a real frame
go test ./internal/scan -bench BenchmarkLiveScan      # scan speed on your store
```

## Regenerating the README images

The README hero shots (`docs/demo.png`, `docs/search.png`) are screenshots of
the live picker. The SVG pipeline is still there if you want a synthetic
store with nothing from your own sessions:

```sh
sh docs/tools/make_images.sh
```

That builds a fabricated store with `docs/tools/make_demo_store.py`, captures
frames through `TestLiveRender`, and converts the ANSI to SVG with
`docs/tools/ansi2svg.py`. To render your own store instead, `TestLiveRender`
honours `CL_RENDER_OUT`, `CL_RENDER_STORE`, `CL_RENDER_COLS`, `CL_RENDER_ROWS`,
`CL_RENDER_CWD`, `CL_RENDER_REPO`, `CL_RENDER_DOWN` and `CL_RENDER_QUERY` —
just don't commit the result.

## A note on performance

Claude uses bounded transcript windows; Codex streams complete rollout records
to keep late prompts and recent metadata discoverable. Neither scanner uses a
cache. If you change how transcripts are read, run the benchmark before and
after. Keep the Claude store well under 100ms; for Codex, compare throughput and
allocations with `go test ./internal/scan -run '^$' -bench BenchmarkCodexLargeRollout`.
