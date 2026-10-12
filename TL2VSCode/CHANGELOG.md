# Changelog

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
