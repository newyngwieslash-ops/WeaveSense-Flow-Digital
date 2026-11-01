import copy
import importlib.util
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location(
    "sbom", Path(__file__).with_name("sbom.py")
)
sbom = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sbom)


class InventoryBoundaryTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        for name in sbom.MANDATORY:
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("fixture attribution only\n")
        self.react = {
            "product_profile": "agriculture",
            "modules": ["src/agriculture/main.tsx"],
            "packages": [{"name": "react", "version": "fixture", "license": "MIT"}],
        }
        self.vue = {
            "profile": "vue-session-boundary",
            "modules": ["./agriculture/main.ts"],
        }

    def test_required_attribution_files_never_inherited_from_root(self):
        sbom.validate_boundary(self.root, self.react, self.vue)
        for name in sbom.MANDATORY:
            with self.subTest(name=name):
                path = self.root / name
                value = path.read_bytes()
                path.unlink()
                with self.assertRaisesRegex(ValueError, "mandatory attribution"):
                    sbom.validate_boundary(self.root, self.react, self.vue)
                path.write_bytes(value)

    def test_missing_reachable_license_fails(self):
        for missing in (None, "", "UNKNOWN", "UNLICENSED", "REVIEW_REQUIRED"):
            graph = copy.deepcopy(self.react)
            graph["packages"][0]["license"] = missing
            with self.assertRaisesRegex(ValueError, "lacks declared license"):
                sbom.validate_boundary(self.root, graph, self.vue)

    def test_drawing_and_legacy_paths_rejected(self):
        for module in (
            "node_modules/tldraw/dist/index.js",
            "node_modules/@tldraw/editor/dist.js",
        ):
            graph = copy.deepcopy(self.react)
            graph["modules"].append(module)
            with self.assertRaisesRegex(ValueError, "drawing dependency"):
                sbom.validate_boundary(self.root, graph, self.vue)
        graph = copy.deepcopy(self.react)
        graph["packages"].append({"name": "@tldraw/editor", "license": "MIT"})
        with self.assertRaisesRegex(ValueError, "drawing dependency"):
            sbom.validate_boundary(self.root, graph, self.vue)
        with self.assertRaisesRegex(ValueError, "legacy business"):
            sbom.validate_boundary(
                self.root,
                self.react,
                {
                    "profile": "vue-session-boundary",
                    "modules": ["./src/store/userStore.ts"],
                },
            )

    def test_notice_bytes_preserved_and_absence_explicit(self):
        inventory = sbom.Inventory(self.root, self.root / "output")
        directory = self.root / "package"
        directory.mkdir()
        self.assertEqual(inventory.texts(directory), [])
        path = directory / "LICENSE"
        path.write_bytes(b"Original copyright\r\nOriginal grant\r\n")
        record = inventory.texts(directory)[0]
        self.assertEqual(
            (inventory.output / record["preserved"]).read_bytes(), path.read_bytes()
        )
        self.assertEqual(record["sha256"], sbom.sha(path))

    def test_farm_profile_requires_exact_api_and_vector_only_map(self):
        graph = {
            "profile": "vue-agriculture-farms",
            "business_api": "same-origin-platform-canonical-v1",
            "map": "openlayers-vector-only-no-external-basemap",
            "modules": ["./agriculture/main.ts", "node_modules/ol/Map.js"],
        }
        sbom.validate_boundary(self.root, self.react, graph)
        for key in ("profile", "business_api", "map"):
            for value in (None, "", "unreviewed"):
                with self.subTest(key=key, value=value):
                    bad = {**graph, key: value}
                    with self.assertRaises(ValueError):
                        sbom.validate_boundary(self.root, self.react, bad)

    def test_profiles_reject_legacy_drawing_and_three_dimensional_modules(self):
        for profile in ("vue-session-boundary", "vue-agriculture-farms"):
            for module in (
                "src/store/userStore.ts", "./src/main.ts",
                r".\src\main.ts", "node_modules/cesium/index.js",
                "node_modules/@cesium/engine/index.js",
                r"node_modules\cesium\index.js",
                "node_modules/tldraw/dist/index.js",
                r"node_modules\@tldraw\editor\dist.js",
            ):
                with self.subTest(profile=profile, module=module):
                    graph = {
                        "profile": profile,
                        "business_api": "same-origin-platform-canonical-v1",
                        "map": "openlayers-vector-only-no-external-basemap",
                        "modules": ["./agriculture/main.ts", module],
                    }
                    with self.assertRaises(ValueError):
                        sbom.validate_boundary(self.root, self.react, graph)


if __name__ == "__main__":
    unittest.main()
