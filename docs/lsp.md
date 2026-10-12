# TL / TL2 language server

`tl2lsp` is a standalone Go executable that speaks the Language Server Protocol
over stdin/stdout. It has no dependency on VS Code, Zed, Node.js, or an editor
runtime. Both editor extensions launch the same executable; parsing, indexing,
diagnostics, navigation and completion live in `internal/tllsp`.

```text
VS Code extension ── LSP / stdio ──┐
                                 ├── tl2lsp ── internal/tlast
Zed extension ────── LSP / stdio ──┘
```

Each editor connection launches its own server process.

## Build and installation

From this checkout:

```sh
make build-lsp
make test-lsp
go install ./cmd/tl2lsp
```

The build produces `target/bin/tl2lsp` (`tl2lsp.exe` on Windows when using
`go build -o target/bin/tl2lsp.exe ./cmd/tl2lsp`). Put the installed executable
on the editor's PATH or configure its absolute path in the editor client.

Once these changes are published in a repository version, installation without
a checkout is also possible with `go install github.com/VKCOM/tl/cmd/tl2lsp@<version>`.

`tl2lsp -version` prints the version. Running without flags serves LSP until
shutdown/exit or stdin closes. Stdout is reserved for protocol messages; server
logs go to stderr.

## Features

- Syntax diagnostics from the existing TL and TL2 lexer/parsers.
- Unknown-type diagnostics, with configurable severity.
- Go to Definition across files, including local template parameters.
- Find References across files, with optional declaration locations.
- Rename types, constructors, functions, variants and local parameters/fields.
- Hover with declarations and documentation comments.
- Completion of primitive types, schema types and local template/field arguments.
- Document symbols with fields, union variants and template parameters.
- Workspace symbol search.
- Full semantic tokens, including comments and declarations.
- Unsaved documents, full/incremental edits and UTF-16 positions.
- Workspace folder changes and file change notifications.

The server supports TL in `.tl` files and TL2 in `.tl2` files, including
references across both formats. For TL, it indexes constructors, result types,
function signatures, templates, masks and repeated fields. Open documents use
the editor's language id (`tl` or `tl2`); unopened files use their extension.
Open buffers take precedence over disk content. Closing a document restores the
indexed disk version if the file is inside the configured schema paths.

## Schema boundaries and settings

By default, all `.tl2` and `.tl` files under workspace folders are indexed, except
`.git`, `node_modules`, `target`, `vendor`, `out`, `.zed` and `.vscode` directories.
Symlinks are not followed. In a repository containing independent schemas or
test fixtures, set `schemaPaths` to the files/directories of one schema and its
dependencies. Relative paths resolve against the first workspace folder.
Open editor documents are indexed even when outside those paths.

Initialization options are editor-independent:

```json
{
  "schemaPaths": ["schemas/api.tl2", "schemas/shared.tl"],
  "diagnostics": { "unknownTypes": "warning" }
}
```

`unknownTypes` accepts `error`, `warning` (default), `information`, `hint` or `off`.
Setting it to `off` leaves syntax diagnostics enabled. This is useful when some
schema dependencies are unavailable locally.

Runtime configuration uses the same object under `settings.tl2` in
`workspace/didChangeConfiguration`. Initialization options and configuration
contain no editor-specific keys.

## Scope and limitations

The recovery parser skips malformed declarations up to the next semicolon,
retaining other valid declarations and their original source positions. An
unfinished declaration is not indexed until it parses successfully. A missing
semicolon can therefore also hide the following declaration. Generator parsing
remains strict and unchanged.

Unknown-type diagnostics check declaration existence and template scope; they
do not perform the full compiler's checks of generic arity, argument categories,
field dependencies, CRC conflicts or binary compatibility. Ambiguous global
definitions return all matching locations. Configure schema boundaries to avoid
unrelated declarations with the same name.

Formatting, code actions and incremental semantic compilation are not
implemented. File notifications rebuild the disk index;
large-workspace performance has not been benchmarked.

## References and rename

`textDocument/references` honors `context.includeDeclaration`. Matches come
from parsed declarations and resolved references; comments, CRC tags and names
in unrelated local scopes are excluded. Several TL constructors declaring the
same result type contribute declaration sites of that one type.

`textDocument/prepareRename` returns the name range and placeholder;
`textDocument/rename` returns a workspace edit without writing files. Clients
supporting `workspace.workspaceEdit.documentChanges` receive document versions
for open buffers and `null` for disk documents. Other clients receive `changes`.
Editor undo/preview and application of the edit stay in the editor.

Qualified names are renamed as a whole (for example, `api.Item` to
`api.Record`). New names must follow TL identifier syntax. Built-ins and
anonymous/ignored fields cannot be renamed. Duplicate global declarations are
ambiguous, except for shared TL result types; narrow `schemaPaths` to one schema.
Global rename requires all indexed schemas to parse successfully so usages in
skipped declarations cannot be missed. A blocked rename lists up to five errors
with absolute file paths and 1-based line/column numbers, including unopened
files. The language server output contains the full list.
References still work in parsed parts
of a malformed file, and local rename remains available in complete declarations.

Before returning edits, the server reparses affected buffers and verifies that
all indexed references retain their resolved declarations. Name collisions and
reference capture are rejected. This uses the LSP index, not the full compiler's
semantic or compatibility validation. Explicit CRC tags are preserved; changing
TL1 declarations can change implicitly calculated constructor CRCs and generated
API names. References outside the configured schema paths are not included.

## Editor clients

- [VS Code setup](../TL2VSCode/README.md)
- [Zed setup](../TL2Zed/README.md)

For another editor, register `.tl` as language id `tl` and `.tl2` as `tl2` and launch `tl2lsp`
using stdio. Send the workspace root and, if necessary, the initialization
options above. Syntax highlighting is available through LSP semantic tokens;
VS Code also retains its TextMate grammar.
