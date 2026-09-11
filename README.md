<div align="center">

# nani

**A folder browser and terminal editor with real LSP — without turning into Neovim.**

[![release](https://img.shields.io/github/v/release/JohnnyBoySou/nani?style=flat-square&color=bd93f9&labelColor=282a36)](https://github.com/JohnnyBoySou/nani/releases)
[![Go](https://img.shields.io/badge/Go-1.27-8be9fd?style=flat-square&labelColor=282a36&logo=go&logoColor=8be9fd)](https://go.dev)
[![platform](https://img.shields.io/badge/Linux-x86__64-50fa7b?style=flat-square&labelColor=282a36&logo=linux&logoColor=50fa7b)](https://github.com/JohnnyBoySou/nani/releases)
[![static binary](https://img.shields.io/badge/binary-static-ff79c6?style=flat-square&labelColor=282a36)](https://github.com/JohnnyBoySou/nani/releases)

<img src="docs/browser.svg" alt="nani browsing folders with the arrow keys and a file preview" width="820">

</div>

## The idea

Walk through the folders with the arrow keys, open a file, edit it, come back to
where you were. No normal mode, no insert mode, no `:wq`, no half hour of config.

The editor is [micro](https://micro-editor.github.io/): `Ctrl+S` saves, `Ctrl+Q`
quits, `Ctrl+C`/`Ctrl+V` copy and paste, the arrows move through the text, and the
shortcut bar stays at the bottom — the same reflexes as nano, with what nano never
had.

> **Why not nano?** It has no LSP support. No plugin system, no channel to talk to
> a language server — only regex syntax highlighting. Autocomplete, go-to-definition
> and errors marked as you type are not possible in it, no matter how much
> `.nanorc` you write.

## The editor

A type error marked on the line, straight from `tsgo` — TypeScript rewritten in Go:

<img src="docs/editor.svg" alt="type error marked on line 5 by tsgo" width="820">

Autocomplete with the function signature, coming from `gopls`:

<img src="docs/autocomplete.svg" alt="gopls autocomplete showing fmt.Append" width="820">

## Install

Download the binary from the [latest release](https://github.com/JohnnyBoySou/nani/releases)
and run the setup:

```bash
chmod +x nani-linux-amd64
./nani-linux-amd64 setup
```

The setup copies the binary itself to `~/.local/bin/nani` — from there on the
command is just `nani`, from any folder. It is idempotent: running it again only
fills in what is missing.

| what | where |
| --- | --- |
| `nani` itself | `~/.local/bin/nani` |
| micro (static binary from the latest release) | `~/.local/bin/micro` |
| plugins `lsp`, `filemanager`, `fzf`, `detectindent`, `editorconfig` | `~/.config/micro/plug/` |
| `gopls` (via `go install`) | `~/go/bin/gopls` |
| `tsgo` (via `npm`, under its own prefix) | `~/.local/lib/tsgo` + link in `~/.local/bin/tsgo` |
| `settings.json` and `bindings.json` | `~/.config/micro/` |

`tsgo` goes under a separate prefix on purpose: it does not touch the machine's
global npm packages.

If you would rather build it:

```bash
go build -o ~/.local/bin/nani .
```

## Usage

```bash
nani            # browses from the current folder (or from ~/work, if called from home)
nani ~/projects # browses from a specific folder
```

Navigation is by arrow keys, like a file manager:

| key | action |
| --- | --- |
| <kbd>→</kbd> | enter the folder (on a file, opens it) |
| <kbd>←</kbd> | go back one level |
| <kbd>Enter</kbd> | open the file, or enter the folder |
| <kbd>Esc</kbd> | quit |

Each level lists folders first and files after, with `../` at the top to go up.
When you come back, the cursor is already on the folder you just left — you can
step in, look around and step out without losing your place. Typing at any point
filters the list.

Closing the editor drops you back in the same folder, ready for the next file.

With `fzf` installed the search is fuzzy and previewed — `bat` for files, `eza`
for folder trees. Without `fzf`, it falls back to a numbered picker that works
anywhere.

### It honours your `.gitignore`

Inside a repository, the listing is the same one git sees: `node_modules`, `dist`,
`.next`, `*.log` and whatever else is ignored simply does not show up — not in the
list, not in the preview. What decides is the project's `.gitignore`, plus
`.git/info/exclude` and your global gitignore.

Outside a repository, a fixed list of never-interesting folders applies
(`node_modules`, `vendor`, `dist`, `build`, `target`, `.venv`, …).

### Editor shortcuts

| key | action |
| --- | --- |
| `Ctrl+S` / `Ctrl+Q` | save / quit |
| `Ctrl+Space` | LSP autocomplete |
| `Alt+k` | hover — type, signature, docs |
| `Alt+d` or `Alt+g` | go to definition |
| `Alt+r` | references |
| `Alt+f` | format (also runs on save) |
| `Ctrl+p` | open file by name |
| `Alt+t` | side file tree |

## How the LSP works here

micro's LSP plugin only understands **push** diagnostics
(`textDocument/publishDiagnostics`). `tsgo` delivers them by **pull**
(`textDocument/diagnostic`) and, on top of that, stays blocked waiting for replies
to requests the plugin never answers. Without a bridge the result is a silent
editor: no error shows up and nothing hints at why.

So `nani` embeds an LSP proxy that sits between the editor and the server:

```
micro  ──stdio──>  nani lsp  ──stdio──>  gopls / tsgo
```

It does three things the plugin does not:

1. **Answers the server's requests** — `workspace/configuration` and
   `client/registerCapability`. That is what unblocks `tsgo`, which waits for them
   before replying to anything else.
2. **Announces the pull capabilities** the editor does not announce, injecting them
   into `initialize`.
3. **Converts pull into push** — on every `didOpen`, `didChange` or `didSave` it
   asks for `textDocument/diagnostic` and publishes the reply as
   `publishDiagnostics`, which the plugin knows how to draw.

`gopls` goes through the same path. A server without pull support answers with an
error, and in that case the bridge keeps the diagnostics it already pushed instead
of wiping them with an empty list.

### The editor opens at the project root

The plugin uses the editor's working directory as `rootUri`. Opened from inside a
subfolder, `gopls` and `tsgo` fail to resolve imports. So `nani` walks up from the
file until it finds `go.mod`, `go.work`, `package.json`, `tsconfig.json`,
`deno.json` or `.git`, and opens the editor from there.

## When something does not work

The proxy writes a log if you point it at a path:

```bash
NANI_LSP_LOG=/tmp/nani-lsp.log nani
```

The log shows the handshake, the requests answered and how many diagnostics came
back on each round:

```
proxy started: tsgo --lsp -stdio
client initialize, pull capabilities injected
answering server request: workspace/configuration
answering server request: client/registerCapability
-> requesting diagnostics for file:///.../index.ts (id nani-diag-1)
<- 1 diagnostic(s) for file:///.../index.ts
```

No diagnostics showing up is usually one of these:

- **the file is outside a project** — with no `go.mod` or `tsconfig.json` around,
  the server does not build a workspace;
- **`typescript` 7 installed as a global npm package** — `typescript-language-server`
  looks for `lib/tsserver.js`, which no longer exists in the version 7 package.
  `nani` uses `tsgo` precisely to avoid depending on that;
- **a binary outside the PATH** — check `which gopls tsgo micro`.

## Environment

| variable | effect |
| --- | --- |
| `NANI_EDITOR` | editor to open instead of micro |
| `NANI_LSP_LOG` | path to the LSP proxy log |

## Layout

```
main.go      CLI subcommands
browser.go   navigation, gitignore-aware listing and opening the editor
lsp.go       LSP bridge: pull -> push and replies to the server's requests
setup.go     installing the editor, plugins, language servers and config
docs/        the captures above, generated from real terminal screens
```
