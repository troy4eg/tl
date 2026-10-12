# TL / TL2 for VS Code

Syntax highlighting and a thin client for the standalone [`tl2lsp`](../docs/lsp.md)
language server. All parsing, navigation, completion and diagnostics run in the
server shared with the Zed client.

## Features

- Automatic language detection and TextMate highlighting for `.tl` (TL) and `.tl2` (TL2).
- Go to Definition and Find References across files and local template parameters.
- Rename types, constructors, functions, variants and local parameters/fields.
- Hover, type completion, document outline and workspace symbols.
- Syntax and configurable unknown-type diagnostics.
- Semantic tokens from the shared server.

See [server features and limitations](../docs/lsp.md) for the exact scope.

## Local setup

1. From the repository root, install the server:

   ```sh
   go install ./cmd/tl2lsp
   ```

   Ensure the Go install directory (`GOBIN`, or `$(go env GOPATH)/bin` when
   `GOBIN` is unset) is on the PATH inherited by VS Code. The extension uses
   `tl2lsp` by default, so no `tl2.server.path` setting is needed in that case.
   After changing PATH, fully restart VS Code so its extension host inherits it.

   Alternatively, `make build-lsp` produces `target/bin/tl2lsp` inside this
   checkout. If that directory is not on PATH, configure its absolute path as
   shown below.

2. Open `TL2VSCode/` in VS Code, then build the extension:

   ```sh
   npm ci
   npm run compile
   ```

3. Press **F5** to open the Extension Development Host and open a `.tl` or `.tl2` file.
   If `tl2lsp` is on VS Code's PATH, it starts automatically with the defaults.
   Optional settings for a local build and explicit schema boundaries:

   ```json
   {
     "tl2.server.path": "/absolute/path/to/tl/target/bin/tl2lsp",
     "tl2.schemaPaths": ["schemas"],
     "tl2.diagnostics.unknownTypes": "warning"
   }
   ```

   Replace `schemas` with your schema directory, or omit `tl2.schemaPaths` to
   index the workspace. Then reload the extension host and open a `.tl` or `.tl2` file.
   Omit `tl2.server.path` when using `tl2lsp` from PATH. This is the extension
   host's PATH; a PATH set only in an integrated terminal is not sufficient.

If startup fails, the extension displays an error with a link to the
**TL / TL2 Language Server** Output channel. Check `tl2lsp -version` and, if
needed, set `tl2.server.path` to the executable's absolute path.

Language features require the server executable. It is installed independently
and is not downloaded or bundled by this extension.

Use **Go to References** / **Find All References** to find usages and
**Rename Symbol** (F2) to rename. Both features come from the shared server.
For qualified names, the rename input contains the complete name (`api.Item`).
See [rename rules](../docs/lsp.md#references-and-rename) for schema boundaries,
collision checks and CRC behavior.

## Settings

| Setting | Default | Purpose |
| --- | --- | --- |
| `tl2.server.path` | `tl2lsp` | Server executable or absolute path |
| `tl2.server.arguments` | `[]` | Arguments passed to the executable |
| `tl2.schemaPaths` | `[]` | Schema files/directories, TL and TL2 schemas |
| `tl2.diagnostics.unknownTypes` | `warning` | `error`, `warning`, `information`, `hint`, `off` |

Relative schema paths resolve against the first workspace folder. Open documents
are included regardless of these paths. For repositories with multiple
independent schemas, configure the paths to one schema and its dependencies.

Schema paths and diagnostic settings update while the server is running. After
changing the executable or arguments, reload the extension host. Use
**TL2: Restart Language Server** to restart the process with its current launch
configuration.

## Packaging and installation

```sh
npm ci
npx @vscode/vsce package
code --install-extension tl2-0.4.0.vsix
```

The extension requires a trusted workspace and VS Code 1.82 or newer. It is not
yet published in the marketplace.

## Editor integration test

After `make build-lsp` from the repository root and `npm ci` / `npm run compile`
from this directory, run against a temporary profile and workspace:

```sh
task_tmp="$(mktemp -d)"
mkdir -p "$task_tmp/workspace"
PATH="$PWD/../target/bin:$PATH" code \
  --user-data-dir "$task_tmp/profile" \
  --extensions-dir "$task_tmp/extensions" \
  --extensionDevelopmentPath="$PWD" \
  --extensionTestsPath="$PWD/test/editor.cjs" \
  --disable-workspace-trust --skip-welcome --skip-release-notes \
  "$task_tmp/workspace"
```

The test exercises file detection, references and rename through VS Code's
providers and applies workspace edits to temporary editor buffers. It checks
shared TL types, bare constructors, TL2 types, local template scope, comments
and explicit CRC tags. Results are saved to `editor-results.json` in the test
workspace. Test sources are excluded from the VSIX.

## Replacing the icon

The icons live in [`icons/`](icons/):

- [`tl2-language-light.png`](icons/tl2-language-light.png) — dark glyph, used on light VS Code themes.
- [`tl2-language-dark.png`](icons/tl2-language-dark.png) — light glyph, used on dark VS Code themes.

To swap them, replace those files (or change the `icon.light`/`icon.dark`
paths in [`package.json`](package.json)). SVG also works and is preferred for
sharp rendering at all sizes.

The icon appears in file tabs and the Outline view. In the Explorer tree it
shows up only if your selected File Icon Theme doesn't define a `.tl2` icon
(most don't), so usually you'll see it.

## Customizing colors

The grammar only tags tokens with **TextMate scope names** (e.g.
`entity.name.type.tl2`, `variable.parameter.field.tl2`). Colors are picked by
your current color theme. Different themes color the same scope very
differently, so if anything looks off — it's the theme, not the extension.

To inspect what is actually under the cursor: `Ctrl+Shift+P` →
**Developer: Inspect Editor Tokens and Scopes**. You'll see both the TextMate
scope and which theme rule produced the color.

If your theme paints fields and types the same color, override them just for
TL2 in your User Settings (`settings.json`):

```jsonc
"editor.tokenColorCustomizations": {
  "textMateRules": [
    {
      "scope": "variable.parameter.field.tl2",
      "settings": { "foreground": "#9CDCFE" }
    },
    {
      "scope": "entity.name.type.tl2",
      "settings": { "foreground": "#4EC9B0" }
    },
    {
      "scope": "entity.name.namespace.tl2",
      "settings": { "foreground": "#DCDCAA", "fontStyle": "italic" }
    },
    {
      "scope": "comment.metadata.crc32.tl2",
      "settings": { "foreground": "#6A6A6A" }
    }
  ]
}
```

### Scopes used by the grammar

| Token | Scope |
| --- | --- |
| Comments | `comment.line.double-slash.tl2`, `comment.block.tl2` |
| Annotation `@read` | `entity.name.tag.annotation.tl2` |
| Built-in types (`int32`, `string`, …) | `support.type.primitive.tl2` |
| Type name in declaration | `entity.name.type.declaration.tl2` |
| Namespace in declaration (left of `.`) | `entity.name.namespace.declaration.tl2` |
| CRC32 magic (`#1b8b9feb`) | `comment.metadata.crc32.tl2` |
| Template parameter (`X` in `<X:Type>`) | `variable.parameter.template.tl2` |
| Template category (`Type`, `#`) | `storage.type.template.category.tl2` |
| Operators `=`, `<=>`, `=>`, `\|`, `?` | `keyword.operator.{assignment,alias,return,union,optional}.tl2` |
| Field name (`name:`) | `variable.parameter.field.tl2` |
| Anonymous field (`_:`) | `variable.language.anonymous.tl2` |
| Type reference (RHS) | `entity.name.type.tl2` |
| Namespace reference (RHS) | `entity.name.namespace.tl2` |
| Union constructor (`\| ctor`) | `entity.name.function.constructor.tl2` |
