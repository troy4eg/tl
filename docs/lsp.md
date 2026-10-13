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

For repeatable performance measurements, run `make bench-lsp`.

The server reuses unchanged parsed files and checks changed documents plus their
semantic dependents. Save/close/watcher events update the indicated file; startup
and schema/workspace configuration changes scan the configured paths. Global
type signatures and constructor tags are indexed per document: edits replace the
changed file's contributions and recheck only affected name/tag groups, including
conflicts with declarations in closed files. Background requests retain an
immutable registry snapshot while later edits publish a new one.
Unchanged diagnostics are skipped, while diagnostics for open buffers retain
their document version.

After initialization, semantic checks run in a separate worker with at most one
active job and one replacement. Edits cancel superseded checks and retain their
pending dependents for the replacement. Results are published only for the same
workspace generation, with no newer buffered text. Navigation uses the current
index immediately. Code actions check the requested document against that index
if its diagnostic publication is still pending. Shutdown drains final diagnostics.

Consecutive typing changes are combined with a 25 ms debounce and a 100 ms
maximum scheduling delay. A request flushes pending changes before using the
index. References, rename, code actions, workspace symbols and document
symbols/tokens run on immutable snapshots in a bounded background queue, leaving
completion, hover and navigation responsive. `$/cancelRequest` cancels queued or
running background requests; rename checks cancellation between documents and
reference checks. A rename prepared for an older workspace is rejected with a
retry message. Other read-only responses can describe the snapshot at request
arrival, as permitted by the [LSP base protocol](https://github.com/microsoft/language-server-protocol/blob/gh-pages/_specifications/lsp/3.17/specification.md#cancellationSupport).

Rename candidates reparse edited buffers, update only their navigation buckets,
and compare bindings in those buffers plus references to the old and new global
names elsewhere. The new-name index includes unresolved references, so narrowing
validation still catches accidental capture in an otherwise unchanged document.

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
- Semantic diagnostics for template arity/categories, duplicate names, field dependencies and CRC conflicts, including unopened files.
- Go to Definition across files, including local template parameters.
- Find References across files, with optional declaration locations.
- Rename types, constructors, functions, variants and local parameters/fields.
- Hover with declarations and documentation comments.
- Context-aware completion of types, template categories, generic arguments, sizes and TL mask bits.
- Declaration/field/array snippets and editable generic argument slots for clients supporting LSP snippets.
- Signature help for generic types, aliases and constructors, including nested applications.
- Quick fixes for type-name typos, legacy TL primitive names in TL2 and missing semicolons.
- Document symbols with fields, union variants and template parameters.
- Workspace symbol search.
- Full semantic tokens, including comments and declarations.
- Unsaved documents, full/incremental edits and UTF-16 positions.
- Navigation, hover, references, completion and outline in unfinished declarations.
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
Setting it to `off` leaves syntax and other semantic diagnostics enabled. This is useful when some
schema dependencies are unavailable locally.

Runtime configuration uses the same object under `settings.tl2` in
`workspace/didChangeConfiguration`. Initialization options and configuration
contain no editor-specific keys.

## Quick fixes

The server exposes `textDocument/codeAction` with kind `quickfix` to clients
declaring `textDocument.codeAction.codeActionLiteralSupport`. VS Code and Zed
display these edits through their standard code action menus. No custom editor
commands or server-side file writes are needed.

- Unknown type names: suggest nearby existing names, including local `Type`
  parameters and primitives, respecting completion's scope/category rules.
  Suggestions preserve existing generic arguments and must not introduce new
  semantic diagnostics in the edited file. Incomplete targets are excluded.
  Up to eight alternatives per occurrence are returned; choosing one replaces
  only that occurrence, rather than renaming a declaration.
- TL primitive names in TL2: suggest `int` → `int32`, `long` → `int64`,
  `float` → `float32`, and `double` → `float64` when the old name is unknown.
  Existing user-defined types with these names are preserved.
- Missing semicolons: insert `;` after the last significant token, before
  trailing comments/whitespace. Reparsing must complete that declaration and
  preserve the surrounding declarations. Unclosed brackets and unfinished
  field types are not repaired this way.

Actions use the current unsaved buffer, match current diagnostics, honor the
requested range and action kind, and include document versions when the client
supports `workspace.workspaceEdit.documentChanges`. Applying, undoing and saving
edits are controlled by the editor. Disabling unknown-type diagnostics also
disables their fixes. Syntax fixes remain available.

## Completion and signature help

Completion distinguishes declaration and field names from type references. Type
positions offer primitive/schema types and local `Type` parameters. Natural-number
positions (TL2 array indexes and `#` generic arguments) offer `#` parameters,
preceding required size fields and a numeric literal. Size fields must have `#`
in TL or `uint32` (including aliases) in TL2. Optional/ignored and later fields
are excluded. Union variants, nested repeats, and RPC request/result fields keep
separate scopes. Unknown generic signatures use a broader list of candidates.
Namespace prefixes replace the whole qualified identifier.

Template declarations offer `Type` and `#`. TL mask completion offers available
mask names and bits `0`–`31`, preserving the mask name and an existing `?`.
Comments do not produce suggestions or signature help.

Clients declaring `textDocument.completion.completionItem.snippetSupport` receive
declaration templates (`struct`, `generic`, `alias`, `union`, `function` for TL2;
`constructor`, `generic`, `function` for TL), field/variant and array/repeat templates.
RPC templates generate a nonzero CRC. Generic type completion supplies editable
type/natural-number argument slots, unless an argument list already follows the
identifier. Other clients receive plain identifier edits without snippet syntax.

Completion responses contain at most 256 items. Larger sets use a
`CompletionList` with `isIncomplete: true`; typing a more specific prefix requests
a fresh matching range, including names omitted from the initial list. Built-ins,
local parameters and templates are included before broad global suggestions.
Clients advertising `completionItem.resolveSupport` for `detail` or
`documentation` receive those properties through `completionItem/resolve`.
Insertion edits and snippet placeholders are already complete in the initial
response and remain unchanged on resolve. Clients without these capabilities
receive the details eagerly. A stale item is returned without resolving it against
a newer schema. This follows the
[LSP completion contract](https://microsoft.github.io/language-server-protocol/specifications/lsp/3.17/specification/#textDocument_completion).

`textDocument/signatureHelp` shows parameter names and `Type`/`#` categories for
the current generic application. It tracks nested angle applications in both
formats, and TL applications in parentheses or function results using whitespace.
Closed applications dismiss their hints; shared TL result signatures are
deduplicated. Missing dependency signatures cannot provide parameter hints.

## Scope and limitations

The editor parser retains names, parameters, fields and type references already
written in unfinished TL and TL2 declarations. Navigation, hover, references,
completion, outline and semantic tokens use those fragments with positions from
the current buffer. Deleted names disappear immediately; there is no stale AST
from the last successful parse. Union variants and nested repeats have their own
completion scopes.

After a syntax error, recovery recognizes explicit declaration headers at the
start of a line and TL section markers, even when a semicolon or closing bracket
is missing. Valid multiline declarations are parsed as a whole first. Recovery
is conservative: ambiguous text and a second declaration on the same line
without a semicolon may not be indexed separately. Generator parsing stays strict.
Semantic checks and CRC calculations use only complete declarations. Known names
of partial types remain available to consumers; signature-dependent checks resume
when their declarations are finished. Syntax diagnostics remain visible while typing.

The semantic pass checks template arity and `Type`/`#` categories, matching TL1
result parameters and shared type signatures, duplicate declarations/parameters/
fields/variants, and references to functions as types. It checks TL1 mask and
repeat-size dependencies, mask bits, and TL2 field-dependent sizes (including
aliases of `uint32`). Dependencies must refer to preceding required fields of the
appropriate type. Nested repeats and union variants retain separate scopes.
It also checks zero TL1 tags and CRC collisions across both formats, using the
same tag checker as the hybrid compiler. The pass runs on recovered ASTs and
unsaved buffers without generating code; missing dependencies do not suppress
unrelated errors. Related locations link conflicts to their declarations.

All indexed files receive diagnostics, including unopened schemas. Removing a
file or narrowing `schemaPaths` clears its stale diagnostics. Closing an indexed
buffer restores diagnostics from disk; changing a dependency updates consumers.

This is not full compiler/backend validation: migration rules, generator-specific
naming policies, complete type instantiation, alias-cycle validation and binary
compatibility checks remain outside the editor's semantic pass. Ambiguous global
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
Rename is unavailable for symbols inside an incomplete declaration. Global rename
requires all indexed schemas to parse successfully so partially parsed usages
cannot be missed. A blocked rename lists up to five errors
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
