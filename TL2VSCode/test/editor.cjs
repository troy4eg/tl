// Run through VS Code's --extensionTestsPath with an empty temporary workspace.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const vscode = require('vscode');

exports.run = async function () {
    const root = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
    const tempRoots = [os.tmpdir(), '/tmp'].filter(p => fs.existsSync(p)).map(p => fs.realpathSync(p));
    assert(root && tempRoots.some(p => fs.realpathSync(root).startsWith(p + path.sep)),
        'The editor test requires a temporary workspace');
    const fixtures = {
        'types.tl': '---types---\nitem#12345678 id:int = Item;\nempty = Item;\nuse value:%item = Use;\n',
        'use.tl2': '// Item 😀\r\nRecord = value:Item;\r\n',
        'alias.tl2': 'Alias <=> Record;\n',
        'local.tl2': 'Generic<T:Type> = x:T; Other<T:Type> = x:T;\n',
        'diagnostics-types.tl2': 'CheckedBox<T:Type> = value:T;\n',
        'diagnostics-use.tl2': '// 😀\r\nSemanticUse = value:CheckedBox<7>;\r\n',
        'typing.tl2': '// 😀\r\nDraft<T:Type> = value:T;\r\nAfterDraft = value:int32;\r\n',
        'typing.tl': '// 😀\r\ndraft {X:Type} value:X = DraftTL X;\r\nafter = AfterTL;\r\n',
    };
    for (const [name, text] of Object.entries(fixtures)) {
        fs.writeFileSync(path.join(root, name), text);
    }
    const documents = {};
    for (const name of Object.keys(fixtures)) {
        if (name === 'diagnostics-use.tl2') continue;
        documents[name] = await vscode.workspace.openTextDocument(path.join(root, name));
    }
    const ext = vscode.extensions.getExtension('brat-vseznamus.tl2');
    assert(ext, 'Extension is not registered');
    await ext.activate();
    assert.equal(documents['types.tl'].languageId, 'tl');
    assert.equal(documents['use.tl2'].languageId, 'tl2');
    const at = (doc, name) => doc.positionAt(doc.getText().indexOf(name));
    async function waitForReferences(doc, name) {
        for (let n = 0; n < 60; n++) {
            const refs = await vscode.commands.executeCommand('vscode.executeReferenceProvider', doc.uri, at(doc, name));
            if (refs?.length) return refs;
            await new Promise(resolve => setTimeout(resolve, 250));
        }
        throw new Error(`References missing for ${name}`);
    }
    async function rename(doc, oldName, newName, expectedEdits) {
        const edit = await vscode.commands.executeCommand('vscode.executeDocumentRenameProvider', doc.uri, at(doc, oldName), newName);
        assert(edit, `Rename edit missing for ${oldName}`);
        const count = edit.entries().reduce((sum, [, edits]) => sum + edits.length, 0);
        assert.equal(count, expectedEdits);
        assert(await vscode.workspace.applyEdit(edit), 'Editor rejected the workspace edit');
        return count;
    }
    const use = documents['use.tl2'];
    await waitForReferences(use, 'Item;');
    // The token position must be inside Item, rather than the field name.
    const itemPosition = use.positionAt(use.getText().indexOf('value:Item') + 6);
    const itemRefs = await vscode.commands.executeCommand('vscode.executeReferenceProvider', use.uri, itemPosition);
    assert(itemRefs.some(ref => ref.uri.toString() === use.uri.toString() && ref.range.start.line === 1));
    const itemEdit = await vscode.commands.executeCommand('vscode.executeDocumentRenameProvider', use.uri, itemPosition, 'Entity');
    assert.equal(itemEdit.entries().reduce((sum, [, edits]) => sum + edits.length, 0), 3);
    assert(await vscode.workspace.applyEdit(itemEdit));
    assert.equal(use.getText(), '// Item 😀\r\nRecord = value:Entity;\r\n');
    assert.equal(documents['types.tl'].getText(), fixtures['types.tl'].replaceAll('Item', 'Entity'));
    await waitForReferences(documents['alias.tl2'], 'Record');
    await rename(documents['alias.tl2'], 'Record', 'Packet', 2);
    assert(use.getText().includes('Packet = value:Entity;'));
    assert.equal(documents['alias.tl2'].getText(), 'Alias <=> Packet;\n');
    await waitForReferences(documents['local.tl2'], 'T:Type');
    await rename(documents['local.tl2'], 'T:Type', 'U', 2);
    assert.equal(documents['local.tl2'].getText(), 'Generic<U:Type> = x:U; Other<T:Type> = x:T;\n');
    await waitForReferences(documents['types.tl'], 'item#');
    await rename(documents['types.tl'], 'item#', 'entry', 2);
    assert(documents['types.tl'].getText().includes('entry#12345678'));
    assert(documents['types.tl'].getText().includes('value:%entry'));
    const closedURI = vscode.Uri.file(path.join(root, 'diagnostics-use.tl2'));
    async function waitForDiagnostics(predicate) {
        for (let n = 0; n < 60; n++) {
            const diagnostics = vscode.languages.getDiagnostics(closedURI);
            if (predicate(diagnostics)) return diagnostics;
            await new Promise(resolve => setTimeout(resolve, 250));
        }
        throw new Error('Closed-file semantic diagnostics did not update');
    }
    const diagnostics = await waitForDiagnostics(ds => ds.some(d => d.code === 'argument-category'));
    const categoryError = diagnostics.find(d => d.code === 'argument-category');
    assert.equal(categoryError.severity, vscode.DiagnosticSeverity.Error);
    assert.equal(categoryError.range.start.line, 1);
    assert.equal(categoryError.relatedInformation[0].location.uri.toString(), documents['diagnostics-types.tl2'].uri.toString());
    assert(!vscode.workspace.textDocuments.some(doc => doc.uri.toString() === closedURI.toString()), 'Consumer should stay unopened');
    const dependency = documents['diagnostics-types.tl2'];
    const dependencyEdit = new vscode.WorkspaceEdit();
    dependencyEdit.replace(dependency.uri, new vscode.Range(dependency.positionAt(0), dependency.positionAt(dependency.getText().length)), 'CheckedBox<N:#> = values:[N]int32;\n');
    assert(await vscode.workspace.applyEdit(dependencyEdit));
    await waitForDiagnostics(ds => ds.length === 0);

    async function replaceBuffer(doc, text) {
        const edit = new vscode.WorkspaceEdit();
        edit.replace(doc.uri, new vscode.Range(doc.positionAt(0), doc.positionAt(doc.getText().length)), text);
        assert(await vscode.workspace.applyEdit(edit));
    }
    async function waitUntil(check, message) {
        for (let n = 0; n < 60; n++) {
            if (await check()) return;
            await new Promise(resolve => setTimeout(resolve, 100));
        }
        throw new Error(message);
    }
    for (const [name, text, parameter, following] of [
        ['typing.tl2', '// 😀\r\nDraft<T:Type> = value:T items:[3]T\r\nAfterDraft = value:int32;\r\n', 'T', 'AfterDraft'],
        ['typing.tl', '// 😀\r\ndraft {X:Type} value:X = DraftTL X\r\nafter = AfterTL;\r\n', 'X', 'after'],
    ]) {
        const doc = documents[name];
        await replaceBuffer(doc, text);
        await waitUntil(async () => vscode.languages.getDiagnostics(doc.uri).some(d => d.code === 'syntax'), 'Missing syntax diagnostic while typing');
        const symbols = await vscode.commands.executeCommand('vscode.executeDocumentSymbolProvider', doc.uri);
        assert.equal(symbols.length, 2);
        assert.equal(symbols[1].name, following, 'Missing semicolon hid the following declaration');
        const refPosition = doc.positionAt(text.indexOf('value:') + 6);
        const defs = await vscode.commands.executeCommand('vscode.executeDefinitionProvider', doc.uri, refPosition);
        assert.equal(defs.length, 1);
        assert.equal(defs[0].uri.toString(), doc.uri.toString());
        assert.equal(defs[0].range.start.line, 1);
        const hovers = await vscode.commands.executeCommand('vscode.executeHoverProvider', doc.uri, refPosition);
        assert(hovers.some(h => h.contents.some(c => c.value.includes(parameter + ':Type'))), 'Hover missing in unfinished declaration');
        const completions = await vscode.commands.executeCommand('vscode.executeCompletionItemProvider', doc.uri, refPosition);
        assert(completions.items.some(i => i.label === parameter), 'Template completion missing while typing');
        const refs = await vscode.commands.executeCommand('vscode.executeReferenceProvider', doc.uri, refPosition);
        assert(refs.length >= 2, 'References missing in unfinished declaration');
        // Insert just the semicolon through the editor; the same declaration becomes complete.
        const insert = new vscode.WorkspaceEdit();
        insert.insert(doc.uri, doc.positionAt(text.indexOf('\r\n', text.indexOf('value:'))), ';');
        assert(await vscode.workspace.applyEdit(insert));
        await waitUntil(async () => vscode.languages.getDiagnostics(doc.uri).length === 0, 'Finishing declaration did not clear diagnostics');
    }
    const typing = documents['typing.tl2'];
    await replaceBuffer(typing, '// 😀\r\nDraft<T:Type> = \r\nAfterDraft = value:int32;\r\n');
    await waitUntil(async () => {
        const syms = await vscode.commands.executeCommand('vscode.executeDocumentSymbolProvider', typing.uri);
        return syms?.[0]?.children.length === 1;
    }, 'Deleted fields remained in the outline');
    const freshCompletions = await vscode.commands.executeCommand('vscode.executeCompletionItemProvider', typing.uri, typing.positionAt(typing.getText().indexOf('= ') + 2));
    assert(freshCompletions.items.some(i => i.label === 'T'));
    assert(!freshCompletions.items.some(i => i.label === 'value' || i.label === 'items'), 'Deleted fields remained in completion');
    // Applying an edit changes editor buffers; save is a separate user action.
    for (const [name, text] of Object.entries(fixtures)) {
        assert.equal(fs.readFileSync(path.join(root, name), 'utf8'), text);
    }
    const result = { version: ext.packageJSON.version, extensionPath: ext.extensionPath,
        referenceProvider: true, tlTypeRename: true, tlConstructorRename: true,
        tl2TypeRename: true, localRename: true, buffersOnly: true,
        closedFileDiagnostics: true, relatedDiagnosticLocations: true, unsavedDependencyDiagnostics: true,
        typingNavigation: true, typingCompletion: true, missingSemicolonRecovery: true, staleFieldsRemoved: true };
    fs.writeFileSync(path.join(root, 'editor-results.json'), JSON.stringify(result));
    console.log('TL editor test:', result);
};
