# TL / TL2 for Zed

A thin client for the standalone [`tl2lsp`](../docs/lsp.md) language server.
The Rust extension only registers TL and TL2 and launches the server. It contains no
TL parser or indexer.

## Setup

1. Build the server from the repository root:

   ```sh
   make build-lsp
   ```

2. In Zed, run **Extensions: Install Dev Extension** and select this `TL2Zed`
   directory. Zed builds the Rust extension; Rust must be installed through
   rustup, with the `wasm32-wasip1` target available.

3. Configure the server executable in Zed settings:

   ```json
   {
     "lsp": {
       "tl2lsp": {
         "binary": { "path": "/absolute/path/to/tl/target/bin/tl2lsp" },
         "initialization_options": {
           "schemaPaths": ["schemas"],
           "diagnostics": { "unknownTypes": "warning" }
         }
       }
     },
     "languages": {
       "TL": { "semantic_tokens": "full" },
       "TL2": { "semantic_tokens": "full" }
     }
   }
   ```

   Replace `schemas` with your schema directory, or omit `schemaPaths` to scan
   the workspace. The extension also finds an installed `tl2lsp` on the
   worktree's PATH if `binary.path` is omitted. `binary.arguments` and
   `binary.env` are forwarded to the process.

4. Open a `.tl` or `.tl2` file. Navigation, references, rename, hover, contextual completion,
   snippets, generic signature help, quick fixes, diagnostics and workspace
   symbols come from the shared server. Syntax highlighting comes from its
   semantic tokens; no separate Tree-sitter grammar is required.

To update server settings while running, Zed's `lsp.tl2lsp.settings` accepts the
same object as `initialization_options`. The extension wraps it under `tl2` for
the server's configuration notification. Restart the language server after
changing its executable or initialization options.

Use Zed's **Find All References** and **Rename** actions on a symbol. The shared
server handles both features, including local scope and cross-file edits.
See [rename rules](../docs/lsp.md#references-and-rename) for restrictions.

Use **editor: toggle code actions** on an error to see quick fixes for type-name
typos, legacy primitive names in TL2 and missing semicolons. See
[quick fix rules](../docs/lsp.md#quick-fixes) for their scope.

## Build verification

```sh
cargo check --locked
cargo build --release --locked --target wasm32-wasip1
```

The WASM artifact is `target/wasm32-wasip1/release/zed_tl2.wasm`. Zed's dev
extension installation builds and places the extension artifact itself.

See [server features and limitations](../docs/lsp.md). This extension is not yet
published in the Zed extension registry and does not download server binaries.
