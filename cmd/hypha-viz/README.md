# hypha-viz

`hypha-viz` is a local knowledge-graph visualization server for Hyphae. It opens the Hyphae SQLite index and serves a force-directed graph viewer at `http://127.0.0.1:7777`. The graph is rendered with a plain HTML5 Canvas — no external JS dependencies, no npm, no CDN. The server shell is built with GoSX; the interactive layer is vanilla JS embedded directly in the page.

## Usage

```
hypha-viz [--addr 127.0.0.1:7777] [--root <hyphae-home>]
```

Run `hypha index rebuild` first to populate the database, then start the server:

```
go run ./cmd/hypha-viz
```
