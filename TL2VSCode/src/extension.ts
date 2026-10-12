import * as vscode from 'vscode';
import { LanguageClient, LanguageClientOptions, ServerOptions } from 'vscode-languageclient/node';

let client: LanguageClient | undefined;

export async function activate(context: vscode.ExtensionContext): Promise<void> {
    const config = vscode.workspace.getConfiguration('tl2');
    const command = config.get<string>('server.path', 'tl2lsp');
    const args = config.get<string[]>('server.arguments', []);
    const serverOptions: ServerOptions = { command, args };
    const clientOptions: LanguageClientOptions = {
        documentSelector: [{ scheme: 'file', language: 'tl' }, { scheme: 'file', language: 'tl2' }],
        initializationOptions: {
            schemaPaths: config.get<string[]>('schemaPaths', []),
            diagnostics: { unknownTypes: config.get<string>('diagnostics.unknownTypes', 'warning') },
        },
        synchronize: {
            configurationSection: 'tl2',
        },
    };
    const output = vscode.window.createOutputChannel('TL / TL2 Language Server');
    context.subscriptions.push(output);
    clientOptions.outputChannel = output;
    client = new LanguageClient('tl2lsp', 'TL / TL2 Language Server', serverOptions, clientOptions);
    context.subscriptions.push(
        vscode.commands.registerCommand('tl2.restartLanguageServer', async () => {
            await client?.restart();
        }),
    );
    try {
        await client.start();
    } catch (error) {
        output.appendLine(String(error));
        const action = await vscode.window.showErrorMessage(
            `Cannot start TL language server (${command}). Check that tl2lsp is executable and on VS Code's PATH, or set tl2.server.path to its absolute path.`,
            'Show Output',
        );
        if (action === 'Show Output') {
            output.show();
        }
    }
}

export async function deactivate(): Promise<void> {
    await client?.stop();
    client = undefined;
}
