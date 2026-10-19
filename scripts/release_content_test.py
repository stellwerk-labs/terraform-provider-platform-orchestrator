#!/usr/bin/env python3
"""Real scanner and bounded archive regressions, no skipped required behavior."""
import io
import gzip
import json
import hashlib
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import unittest
import zipfile
import zlib

import release_content as content


ROOT = Path(__file__).resolve().parents[1]


def gzip_member(raw, name=b"", comment=b"", extra=b"", header_crc=False):
    flags = (8 if name else 0) | (16 if comment else 0) | (4 if extra else 0) | (2 if header_crc else 0)
    header = b"\x1f\x8b\x08" + bytes([flags]) + b"\0" * 6
    if extra:
        header += len(extra).to_bytes(2, "little") + extra
    if name:
        header += name + b"\0"
    if comment:
        header += comment + b"\0"
    if header_crc:
        header += (zlib.crc32(header) & 0xffff).to_bytes(2, "little")
    compressor = zlib.compressobj(wbits=-15)
    return (header + compressor.compress(raw) + compressor.flush()
            + zlib.crc32(raw).to_bytes(4, "little") + (len(raw) & 0xffffffff).to_bytes(4, "little"))


class ReleaseContentTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if shutil.which("gitleaks") is None:
            raise RuntimeError("pinned gitleaks 8.30.1 is required, run scripts/install-release-tools.sh")
        if subprocess.check_output(["gitleaks", "version"], text=True).strip() != "8.30.1":
            raise RuntimeError("pinned gitleaks 8.30.1 is required")

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.shipping = self.root / "shipping"
        self.shipping.mkdir()

    def scan(self, *literals, script=None):
        return subprocess.run(
            ["bash", str(script or ROOT / "scripts/scan-release-content.sh"), str(self.shipping), *literals],
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False,
        )

    @staticmethod
    def token():
        return ("gh" + "p_" + "4Ax9eC2mF7qR5uT8vW3zB6dH1jK0nP9sY2aL").encode()

    def test_real_scanner_rejects_binary_secret_and_inline_bypass(self):
        path = self.shipping / "application"
        path.write_bytes(b"\x7fELF\x02\x01\0" + self.token() + b" # gitleaks:allow\0")
        result = self.scan()
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn(self.token(), result.stdout + result.stderr)

    def test_real_scanner_ignores_source_controlled_ignore_file(self):
        path = self.shipping / "fixture.txt"
        path.write_bytes(self.token() + b"\n")
        report = self.root / "report.json"
        result = subprocess.run(
            ["gitleaks", "dir", "--redact", "--no-banner", "--log-level", "error",
             "--report-format", "json", "--report-path", str(report), "."],
            cwd=self.shipping, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False,
        )
        self.assertEqual(result.returncode, 1)
        findings = json.loads(report.read_text())
        self.assertTrue(findings)
        (self.shipping / ".gitleaksignore").write_text("\n".join(f["Fingerprint"] for f in findings))
        self.assertNotEqual(self.scan().returncode, 0)

    def test_real_scanner_rejects_text_inline_allow(self):
        (self.shipping / "fixture.txt").write_bytes(self.token() + b" # gitleaks:allow\n")
        self.assertNotEqual(self.scan().returncode, 0)

    def test_invalid_scanner_configuration_fails_closed(self):
        scripts = self.root / "tool/scripts"
        scripts.mkdir(parents=True)
        for name in ("scan-release-content.sh", "release-tools.sh", "release_content.py"):
            shutil.copyfile(ROOT / "scripts" / name, scripts / name)
        (scripts / "gitleaks.toml").write_text("this is not TOML [")
        self.assertNotEqual(self.scan(script=scripts / "scan-release-content.sh").returncode, 0)

    def test_paths_metadata_and_archive_contents_are_scanned(self):
        (self.shipping / "release.provenance.json").write_text('{"source":"private-owner-fixture"}')
        self.assertNotEqual(self.scan("private-owner-fixture").returncode, 0)
        (self.shipping / "release.provenance.json").unlink()
        with zipfile.ZipFile(self.shipping / "payload.zip", "w") as archive:
            archive.writestr("licenses/private-owner-fixture/NOTICE", "Public harmless notice")
        self.assertNotEqual(self.scan("private-owner-fixture").returncode, 0)
        (self.shipping / "payload.zip").unlink()
        with tarfile.open(self.shipping / "payload.tar.gz", "w:gz") as archive:
            member = tarfile.TarInfo("application")
            payload = b"\x7fELF\0" + self.token() + b"\0"
            member.size = len(payload)
            archive.addfile(member, io.BytesIO(payload))
        self.assertNotEqual(self.scan().returncode, 0)

    def test_archive_metadata_has_aggregate_byte_bound(self):
        # Empty tar archives still carry 10 KiB of headers/padding each. Their
        # metadata must share the extraction budget, despite having no bodies.
        for name in ("first.tar", "second.tar"):
            with tarfile.open(self.shipping / name, "w"):
                pass
        with self.assertRaisesRegex(ValueError, "archive metadata exceeds scan limit"):
            content.unpack(self.shipping, self.root / "unpacked", self.root / "metadata", max_bytes=15000)

    def test_bounded_unpack_rejects_unsafe_members_and_size(self):
        for kind in ("traversal", "link", "duplicate"):
            path = self.shipping / "payload.tar"
            with tarfile.open(path, "w") as archive:
                member = tarfile.TarInfo("../outside" if kind == "traversal" else "entry")
                if kind == "link":
                    member.type = tarfile.SYMTYPE
                    member.linkname = "../outside"
                archive.addfile(member)
                if kind == "duplicate":
                    archive.addfile(member)
            with self.assertRaisesRegex(ValueError, "unsupported archive scan entry" if kind == "link" else "unsafe or duplicate"):
                content.unpack(self.shipping, self.root / kind, max_bytes=20 * 1024)
            path.unlink()
        with zipfile.ZipFile(self.shipping / "payload.zip", "w", compression=zipfile.ZIP_DEFLATED) as archive:
            archive.writestr("oversized", b"x" * 251)
        with self.assertRaisesRegex(ValueError, "content exceeds scan limit"):
            content.unpack(self.shipping, self.root / "bounded", max_bytes=250)

    def test_gzip_optional_metadata_in_every_member_is_scanned(self):
        raw = io.BytesIO()
        with tarfile.open(fileobj=raw, mode="w") as archive:
            entry = tarfile.TarInfo("notice")
            entry.size = 6
            archive.addfile(entry, io.BytesIO(b"public"))
        for field in ("name", "comment", "extra"):
            with self.subTest(field=field):
                data = gzip_member(raw.getvalue()[:512], name=b"public-base-layer", header_crc=True) + gzip_member(
                    raw.getvalue()[512:], header_crc=True, **{field: self.token() + b" # gitleaks:allow"})
                (self.shipping / "payload.tar.gz").write_bytes(data)
                result = self.scan()
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn(self.token(), result.stdout + result.stderr)

    def test_gzip_member_validation_and_bounds(self):
        first = gzip_member(b"first", name=b"base", comment=b"public", extra=b"metadata", header_crc=True)
        second = gzip_member(b"second")
        raw, metadata = content.decode_gzip_members(first + b"\0\0" + second + b"\0")
        self.assertEqual(raw, b"firstsecond")
        self.assertIn(b"base\0public\0", metadata)
        self.assertIn(b"metadata", metadata)
        self.assertTrue(metadata.endswith(b"\0"))
        for malformed in (b"", b"\0" + first, first[:-1], first + b"not-gzip", first[:3] + b"\xe0" + first[4:],
                          first[:-8] + bytes([first[-8] ^ 1]) + first[-7:],
                          first[:-4] + bytes([first[-4] ^ 1]) + first[-3:],
                          first[:10] + b"\xff\xff" + first[12:],
                          gzip_member(b"data", name=b"name")[:12]):
            with self.subTest(malformed=hashlib.sha256(malformed).hexdigest()), self.assertRaises(ValueError):
                content.decode_gzip_members(malformed)
        for options in ({"max_output": 4}, {"max_metadata": 8}, {"max_members": 1}, {"max_members": 0}):
            with self.subTest(options=options), self.assertRaises(ValueError):
                content.decode_gzip_members(first + second, **options)


    def test_compressed_tar_pax_and_trailing_bytes_are_scanned(self):
        for kind in ("pax", "trailing"):
            with self.subTest(kind=kind):
                raw = io.BytesIO()
                with tarfile.open(fileobj=raw, mode="w") as archive:
                    entry = tarfile.TarInfo("notice")
                    if kind == "pax":
                        entry.pax_headers = {"public.metadata": self.token().decode() + " # gitleaks:allow"}
                    entry.size = 6
                    archive.addfile(entry, io.BytesIO(b"public"))
                data = raw.getvalue() + (b"\0" + self.token() + b"\0" if kind == "trailing" else b"")
                (self.shipping / "payload.tar.gz").write_bytes(gzip.compress(data, mtime=0))
                result = self.scan()
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn(self.token(), result.stdout + result.stderr)

    def test_clean_content_and_shipping_paths_pass(self):
        path = self.shipping / "licenses/example.com/module"
        path.mkdir(parents=True)
        (path / "LICENSE").write_text("Public legal notice\n")
        self.assertEqual(self.scan("private-owner-fixture").returncode, 0)

    def test_checksum_exception_requires_complete_actual_content_proof(self):
        file = self.shipping / "api_key"
        file.write_text("Public redistribution file\\n")
        manifest = self.shipping / "notices.sha256.json"
        manifest.write_text(json.dumps({file.name: hashlib.sha256(file.read_bytes()).hexdigest()}))
        result = self.scan()
        self.assertEqual(result.returncode, 0)
        self.assertIn(b"checksum findings", result.stderr)
        file.write_text("changed public bytes\\n")
        self.assertNotEqual(self.scan().returncode, 0)
        file.write_bytes(self.token())
        manifest.write_text(json.dumps({file.name: hashlib.sha256(file.read_bytes()).hexdigest()}))
        self.assertNotEqual(self.scan().returncode, 0)

    def test_public_documentation_exception_requires_every_exact_boundary(self):
        metadata = json.loads(subprocess.check_output([
            "go", "mod", "download", "-json", content.PUBLIC_LOG_MODULE + "@" + content.PUBLIC_LOG_VERSION,
        ], text=True))
        self.assertEqual(metadata["Sum"], content.PUBLIC_LOG_SUM)
        bundle = self.shipping / "licenses"
        target = bundle / content.PUBLIC_LOG_MODULE / "tfsdklog/options_test.go"
        target.parent.mkdir(parents=True)
        original = (Path(metadata["Dir"]) / "tfsdklog/options_test.go").read_bytes()
        target.write_bytes(original)
        self.assertEqual(hashlib.sha256(original).hexdigest(), content.PUBLIC_LOG_FIXTURES["tfsdklog/options_test.go"])
        graph = {"linux/amd64": {content.PUBLIC_LOG_MODULE: content.PUBLIC_LOG_VERSION}}
        (bundle / "modules.json").write_text(json.dumps(graph))
        source = self.root / "source"
        source.mkdir()
        good_sum = f"{content.PUBLIC_LOG_MODULE} {content.PUBLIC_LOG_VERSION} {content.PUBLIC_LOG_SUM}\n"
        (source / "go.sum").write_text(good_sum)
        self.assertTrue(content.documented_public_path(target, source))
        self.assertEqual(self.scan().returncode, 0)
        target.write_bytes(original + b"changed")
        self.assertFalse(content.documented_public_path(target, source))
        self.assertNotEqual(self.scan().returncode, 0)
        target.write_bytes(original)
        graph["linux/amd64"][content.PUBLIC_LOG_MODULE] = "v0.9.1"
        (bundle / "modules.json").write_text(json.dumps(graph))
        self.assertFalse(content.documented_public_path(target, source))
        graph["linux/amd64"][content.PUBLIC_LOG_MODULE] = content.PUBLIC_LOG_VERSION
        (bundle / "modules.json").write_text(json.dumps(graph))
        (source / "go.sum").write_text(good_sum.replace(content.PUBLIC_LOG_SUM, "changed"))
        self.assertFalse(content.documented_public_path(target, source))
        renamed = target.with_name("different_test.go")
        target.rename(renamed)
        self.assertFalse(content.documented_public_path(renamed, source))

    def test_exact_public_secret_fixture_manifest_and_drift(self):
        records = json.loads((ROOT / "scripts/known-public-fixtures.json").read_text())
        graphs = {"linux/amd64": {record["module"]: record["version"] for record in records}}
        bundle = self.shipping / "licenses"
        bundle.mkdir()
        (bundle / "modules.json").write_text(json.dumps(graphs))
        files = []
        for record in records:
            metadata = json.loads(subprocess.check_output(["go", "mod", "download", "-json", record["module"] + "@" + record["version"]], text=True))
            self.assertEqual(metadata["Sum"], record["sum"])
            relative = record["path"].removeprefix("licenses/" + record["module"] + "/")
            data = (Path(metadata["Dir"]) / relative).read_bytes()
            self.assertEqual(hashlib.sha256(data).hexdigest(), record["sha256"])
            target = self.shipping / record["path"]
            target.parent.mkdir(parents=True)
            target.write_bytes(data)
            files.append((target, data))
        result = self.scan()
        self.assertEqual(result.returncode, 0)
        self.assertIn(b"accepted 13 exact", result.stderr)
        target, data = files[0]
        target.write_bytes(data + b"\n" + self.token())
        self.assertNotEqual(self.scan().returncode, 0)
        target.write_bytes(data + b"\nchanged file")
        self.assertNotEqual(self.scan().returncode, 0)
        target.write_bytes(data)
        graphs["linux/amd64"][records[0]["module"]] = "v0.0.0"
        (bundle / "modules.json").write_text(json.dumps(graphs))
        self.assertNotEqual(self.scan().returncode, 0)


if __name__ == "__main__":
    unittest.main()
