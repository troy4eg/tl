# Changelog

## 0.5.0

- Keep navigation, hover, references, completion and outline available in unfinished TL and TL2 declarations.
- Recover following declarations after missing semicolons or unclosed brackets, preserving current buffer positions.
- Retain variant and nested-repeat completion scopes; remove deleted fields immediately.
- Defer semantic checks and rename for incomplete declarations until they are finished.

## 0.4.0

- Diagnose template arity/category errors, duplicate declarations, local names and variants, invalid field dependencies, and CRC conflicts in TL and TL2.
- Publish syntax and semantic diagnostics for unopened indexed schemas and clear stale diagnostics when files leave the schema.
- Update dependent-file diagnostics from unsaved buffers and link conflicts to their original declarations.

## 0.3.2

- Accept the legacy TL declaration `_ {X:Type} result:X = ReqResult X;` in the editor parser without breaking internal builtin placeholders.

## 0.3.1

- Report the file path, line, column and parser error when syntax errors block rename.
- Show up to five errors in the message and log the complete list to the server output.

## 0.3.0

- Find references across TL and TL2 schemas, including local parameters and fields.
- Rename symbols with prepareRename and workspace edits, including shared TL result types.
- Reject ambiguous names, name collisions, reference capture and invalid identifiers.
- Use versioned edits for clients that support documentChanges.

## 0.2.0

- Register `.tl` as TL alongside `.tl2` as TL2 and connect both to the server.
- Add TL navigation, hover, completion, symbols and syntax diagnostics.
- Honor manually selected document languages, including after edits.
- Show an actionable error and Output channel when the server cannot start.

## 0.1.0

- Language features now use the standalone `tl2lsp` server over stdio.
- Shared syntax diagnostics, workspace symbols and semantic tokens.
- Configurable server executable, arguments and schema paths.
- Removed the editor-specific parser and indexer.

## 0.0.1

- Initial release.
- Syntax highlighting for `.tl2` files (TextMate grammar in JSON).
- Language icon for `.tl2` files.
- Document Symbol Provider (Outline / Go to Symbol): structs, unions with
  constructors as children, aliases, functions.
- Hover Provider: declaration signature + leading `//` comments.
- Definition Provider: Go to Definition / Ctrl+Click on a type reference,
  resolves cross-file within the workspace.
- Completion Provider: built-in primitives + every declared type in the
  workspace, with signature and doc-comment in the popup.
- Diagnostics: references to types not declared anywhere are marked as
  "Unknown type". Severity configurable via `tl2.diagnostics.unknownTypes`.
