# Output formats

Every `hypha` command emits a uniform JSON envelope when asked for
machine-readable output. There are four formats — the right one depends
on whether a human or an agent is reading.

## The envelope

```json
{
  "ok": true,
  "command": "recall",
  "hyphae_version": "0.1.8",
  "schema": 1,
  "data": { /* command-specific payload */ },
  "warnings": [],
  "errors": []
}
```

| Field | Meaning |
| --- | --- |
| `ok` | `true` on success, `false` on error. Mirrors the exit code. |
| `command` | The command that produced this response (`recall`, `pulse`, `graft`, …). |
| `hyphae_version` | Running binary version. |
| `schema` | Envelope schema version. Bumps only when the envelope or short-key map changes. Starts at `1`. |
| `data` | Command-specific payload. |
| `warnings` | Array of `Note` (`code`, `message`, `hint`, `path`). Always present, possibly empty. |
| `errors` | Array of `Note`. Always present; non-empty iff `ok: false`. |

A `Note` looks like:

```json
{
  "code": "NOT_FOUND",
  "message": "no object with id X",
  "hint": "try `hypha recall \"X\"`",
  "path": "data.id"
}
```

`code` and `hint` are optional; `message` is always present.

## The four formats

Pick with `--format text|json|compact|jsonline`. The default
auto-detects.

| Format | Bytes | Readable | Use when |
| --- | --- | --- | --- |
| `text` | (varies) | human | Reading at a terminal |
| `json` | baseline | yes | Debugging, jq inspection |
| `jsonline` | ~70% of `json` | yes | Pipes, agents that prefer self-describing keys |
| `compact` | ~50–70% of `json` | with docs | Hot-path agent calls |

### `text`

Human-readable. The format hyphae was rendering in v0.1.7 and earlier.

```
$ hypha recall "graft" --format text
Found 6 matches in m31labs/horizon. Top: Horizon v0.1.0 …
  hypha://m31labs/horizon/object/plan.horizon.v0.1.0  Horizon v0.1.0 …
      …Commits use the user's preferred Orchard stack: `buckley commit
        ↳ hypha://…#horizon-v0-1-0-implementation-plan  (L3-3)
```

### `json`

Full-key indented JSON envelope. Good for debugging or piping to `jq`.

```
$ hypha recall "graft" --format json
{
  "ok": true,
  "command": "recall",
  "hyphae_version": "0.1.8",
  "schema": 1,
  "data": {
    "summary": "...",
    "hits": [
      {
        "uri": "hypha://...",
        "title": "...",
        "snippets": [
          {
            "text": "...",
            "citation": {
              "anchor": "hypha://...#slug",
              "line": 42,
              "end_line": 44
            }
          }
        ]
      }
    ]
  },
  "warnings": [],
  "errors": []
}
```

### `jsonline`

Single-line full-key JSON. ~30% fewer bytes than `json` (no
indentation, no per-key newlines). Self-describing.

```
$ hypha recall "graft" --format jsonline
{"ok":true,"command":"recall","hyphae_version":"0.1.8","schema":1,"data":{"summary":"...","hits":[...]},"warnings":[],"errors":[]}
```

### `compact`

Single-line, short-key envelope. Same data as `json`/`jsonline`, with
keys remapped through a documented short-key table.

```
$ hypha recall "graft" --format compact
{"ok":1,"c":"recall","v":"0.1.8","s":1,"d":{"su":"...","hs":[...]},"w":[],"e":[]}
```

Trade-off: smallest payload, but the receiver has to know the
key-map. Documented in `internal/envelope/keys.go`. Common short keys:

| Full key | Short |
| --- | --- |
| `command` | `c` |
| `hyphae_version` | `v` |
| `schema` | `s` |
| `data` | `d` |
| `warnings` | `w` |
| `errors` | `e` |
| `code` | `co` |
| `message` | `m` |
| `hint` | `h` |
| `path` | `p` |
| `query` | `q` |
| `summary` | `su` |
| `hits` | `hs` |
| `uri` | `u` |
| `title` | `t` |
| `score` | `sc` |
| `snippets` | `sn` |
| `text` | `tx` |
| `citation` | `ci` |
| `anchor` | `an` |
| `line` | `ln` |
| `end_line` | `el` |

The full table is the canonical source. Unmapped keys pass through
unchanged.

## Default selection

When you don't pass `--format`:

1. If `HYPHAE_FORMAT` is set and parses, use it.
2. Else if stdout is a TTY, use `text`.
3. Else use `compact`.

So:

```bash
hypha recall "X"                  # at a TTY → text
hypha recall "X" | jq .           # piped → compact (but pipe through jq might choke on short keys; use jsonline below)
HYPHAE_FORMAT=jsonline hypha recall "X" | jq .   # piped + parse-friendly
```

For agent integrations that want predictable parsing without learning
the short-key map, set `HYPHAE_FORMAT=jsonline` and forget about it.

## Errors

Errors flip `ok` to `false`, populate `errors[]`, and exit non-zero:

```json
{
  "ok": false,
  "command": "show",
  "hyphae_version": "0.1.8",
  "schema": 1,
  "warnings": [],
  "errors": [
    {
      "code": "NOT_FOUND",
      "message": "no object with id \"concept.missing\"",
      "hint": "try `hypha recall \"missing\"`"
    }
  ]
}
```

In `text` mode the same condition prints:

```
error: no object with id "concept.missing"
  hint: try `hypha recall "missing"`
```

## Warnings

Warnings are advisory. The classic case: token-budgeted responses that
got trimmed.

```json
{
  "ok": true,
  "command": "recall",
  "data": { /* fewer rows than asked for */ },
  "warnings": [
    {
      "code": "TRUNCATED",
      "message": "dropped 3 trailing row(s) to fit max_tokens=800",
      "hint": "increase max_tokens or narrow the filter to see more"
    }
  ],
  "errors": []
}
```

## Exit codes

| Exit | Meaning |
| --- | --- |
| `0` | Success |
| `1` | Error (any error). The `errors[]` array carries the detail. |

## Parsing tips

- Read `ok` first. If `false`, look at `errors[0]`.
- Read `warnings[]` even on success — `TRUNCATED` and `STALE` warnings
  affect interpretation.
- Pin `schema` if you embed Hyphae in long-lived tooling: today's
  schema is `1`. A future `2` would be a breaking change.
- For agents that don't speak the short-key map, set
  `HYPHAE_FORMAT=jsonline`.
- For agents that do (lower token cost, you have the map): use
  `compact`.
