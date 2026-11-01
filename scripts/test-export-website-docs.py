import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('exporter', Path(__file__).with_name('export-website-docs.py'))
exporter = importlib.util.module_from_spec(spec)
spec.loader.exec_module(exporter)


class DocumentationExport(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.bundle = exporter.export()

    def test_all_help_commands_exported(self):
        names = {command['name'] for command in self.bundle['commands']}
        self.assertTrue({'doctor', 'prepare', 'destroy', 'example', 'schema', 'daemon'} <= names)
        for command in self.bundle['commands']:
            self.assertTrue(command['help'].startswith('Usage: fugato ' + command['name']))
            self.assertTrue(command['summary'])

    def test_complete_sources_and_unique_routes(self):
        paths = {guide['path'] for guide in self.bundle['guides']}
        self.assertTrue({'docs/reference/environment.md', 'docs/reference/mcp.md', 'docs/guides/agent-setup.md'} <= paths)
        self.assertEqual(len(paths), len(self.bundle['guides']))
        self.assertTrue(paths <= self.bundle['sources'].keys())
        self.assertEqual(set(self.bundle['examples']), {'native', 'compose', 'mixed'})
        self.assertEqual(self.bundle['schema']['properties']['version']['const'], 1)


if __name__ == '__main__':
    unittest.main()
