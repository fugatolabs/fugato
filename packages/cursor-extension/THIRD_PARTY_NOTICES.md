# Third-party inventory for the Cursor extension

The Fugato VSIX contains Fugato JavaScript, configuration schema, and artwork
under the accompanying Apache-2.0 LICENSE and NOTICE. It bundles no npm runtime
packages. The extension imports the VS Code editor API and Node built-ins
provided by the editor; those runtimes are distributed by the editor vendor.

TypeScript, @types/node, @types/vscode, @vscode/test-electron, @vscode/vsce and
their transitive dependencies are development/build tools. They are excluded
from the VSIX by `.vscodeignore` and `vsce package --no-dependencies`.
The separately installed Fugato CLI/MCP binaries include their own complete
THIRD_PARTY_NOTICES.md and third_party directory in each download archive.
