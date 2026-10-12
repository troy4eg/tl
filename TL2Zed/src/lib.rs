use zed_extension_api::{self as zed, settings::LspSettings, Result};

struct Tl2Extension;

impl zed::Extension for Tl2Extension {
    fn new() -> Self {
        Self
    }

    fn language_server_command(
        &mut self,
        server_id: &zed::LanguageServerId,
        worktree: &zed::Worktree,
    ) -> Result<zed::Command> {
        if server_id.as_ref() != "tl2lsp" {
            return Err(format!("Unknown language server: {server_id}"));
        }
        let settings = LspSettings::for_worktree(server_id.as_ref(), worktree)?;
        let binary = settings.binary.unwrap_or(zed::settings::CommandSettings {
            path: None,
            arguments: None,
            env: None,
        });
        let command = binary.path.or_else(|| worktree.which("tl2lsp")).ok_or_else(|| {
            "Install tl2lsp with `go install ./cmd/tl2lsp` from the TL repository root, or set lsp.tl2lsp.binary.path in Zed settings".to_string()
        })?;
        let mut env = worktree.shell_env();
        if let Some(overrides) = binary.env {
            for (key, value) in overrides {
                env.retain(|(name, _)| name != &key);
                env.push((key, value));
            }
        }
        Ok(zed::Command {
            command,
            args: binary.arguments.unwrap_or_default(),
            env,
        })
    }

    fn language_server_initialization_options(
        &mut self,
        server_id: &zed::LanguageServerId,
        worktree: &zed::Worktree,
    ) -> Result<Option<zed::serde_json::Value>> {
        LspSettings::for_worktree(server_id.as_ref(), worktree)
            .map(|settings| settings.initialization_options)
    }

    fn language_server_workspace_configuration(
        &mut self,
        server_id: &zed::LanguageServerId,
        worktree: &zed::Worktree,
    ) -> Result<Option<zed::serde_json::Value>> {
        LspSettings::for_worktree(server_id.as_ref(), worktree).map(|settings| {
            settings
                .settings
                .map(|options| zed::serde_json::json!({ "tl2": options }))
        })
    }
}

zed::register_extension!(Tl2Extension);
