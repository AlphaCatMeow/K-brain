import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("prepare_cua", Path(__file__).with_name("prepare-cua.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class ArchivePaths(unittest.TestCase):
    def test_root_and_file(self):
        self.assertIsNone(module.safe_name("release/"))
        self.assertEqual(module.safe_name("release/CuaDriver.app/Contents/MacOS/cua-driver"),
                         "CuaDriver.app/Contents/MacOS/cua-driver")

    def test_unsafe_paths(self):
        for name in ["../x", "release/../../x", "/absolute/x", "C:/x", "release\\x"]:
            with self.subTest(name=name), self.assertRaises(ValueError):
                module.safe_name(name)


if __name__ == "__main__":
    unittest.main()
