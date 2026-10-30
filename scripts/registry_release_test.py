"""Real file/GPG tests of Registry projection and full signed provider closure."""
import importlib.util
import json
import os
import re
from pathlib import Path
import subprocess
import tempfile
import unittest

import qualify_release as release

SPEC = importlib.util.spec_from_file_location("qualified_signer", Path(__file__).with_name("sign-qualified-artifact.py"))
signer = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(signer)
TARGETS = ("darwin_amd64", "darwin_arm64", "freebsd_386", "freebsd_amd64", "freebsd_arm", "freebsd_arm64",
           "linux_386", "linux_amd64", "linux_arm", "linux_arm64", "windows_386", "windows_amd64", "windows_arm64")


class RegistryReleaseTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.keys = tempfile.TemporaryDirectory(prefix="provider-test-key-", dir="/private/tmp" if Path('/private/tmp').is_dir() else None)
        cls.previous_home = os.environ.get("GNUPGHOME")
        cls.previous_phrase = os.environ.get("GPG_PASSPHRASE")
        os.environ.update(GNUPGHOME=cls.keys.name, GPG_PASSPHRASE="")
        subprocess.run(["gpg", "--batch", "--pinentry-mode", "loopback", "--passphrase", "", "--quick-generate-key",
                        "Provider test fixture <fixture@example.com>", "rsa2048", "sign", "0"], check=True, capture_output=True, timeout=30)
        keys = subprocess.check_output(["gpg", "--batch", "--with-colons", "--list-secret-keys"], text=True, stderr=subprocess.DEVNULL)
        cls.fingerprint = next(line.split(":")[9] for line in keys.splitlines() if line.startswith("fpr:"))

    @classmethod
    def tearDownClass(cls):
        subprocess.run(["gpgconf", "--kill", "gpg-agent"], check=True, capture_output=True, timeout=10)
        for name, previous in (("GNUPGHOME", cls.previous_home), ("GPG_PASSPHRASE", cls.previous_phrase)):
            if previous is None:
                os.environ.pop(name, None)
            else:
                os.environ[name] = previous
        cls.keys.cleanup()

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="provider-contract-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.prefix = "terraform-provider-platform-orchestrator_2.1.0-rc.2"
        files = {}
        for target in TARGETS:
            for suffix in ("", ".spdx.json", ".provenance.json"):
                name = self.prefix + "_" + target + ".zip" + suffix
                (self.root / name).write_bytes((name + "\n").encode())
                files[name] = release.sha(self.root / name)
        manifest = self.prefix + "_manifest.json"
        (self.root / manifest).write_text('{"version":1,"metadata":{"protocol_versions":["6.0"]}}\n')
        files["terraform-registry-manifest.json"] = release.sha(self.root / manifest)
        self.receipt = {"source": {"version": "2.1.0-rc.2"}, "files": files}
        (self.root / "release-content.json").write_text(json.dumps(self.receipt))
        self.checksum = self.root / (self.prefix + "_SHA256SUMS")
        expected = release.published_files(self.receipt)
        self.registry = {n: h for n, h in expected.items() if n.endswith('.zip') or n == manifest}
        self.checksum.write_text(''.join(h + '  ' + n + '\n' for n, h in sorted(self.registry.items())))

    def sign(self):
        for artifact in (self.checksum, self.root / 'release-content.json'):
            signer.sign_gpg(artifact, self.root / (artifact.name + '.sig'), self.fingerprint)

    def test_both_real_signatures_and_complete_44_asset_consumer(self):
        self.sign()
        self.assertEqual(len(list(self.root.iterdir())), 44)
        self.assertEqual(len(self.registry), 14)
        self.assertEqual(len(self.receipt['files']), 40)
        self.assertEqual(signer.verify_provider_release(self.root, self.fingerprint, {'version': '2.1.0-rc.2'}), self.receipt)

    def test_registry_rejects_metadata_unknown_duplicate_missing_and_tamper(self):
        valid = self.checksum.read_text()
        metadata = next(n for n in self.receipt['files'] if n.endswith('.spdx.json'))
        variants = [valid + self.receipt['files'][metadata] + '  ' + metadata + '\n',
                    valid + '0' * 64 + '  unknown.zip\n', valid + valid.splitlines()[0] + '\n',
                    '\n'.join(valid.splitlines()[1:]) + '\n', valid.replace(valid[:64], '0' * 64, 1)]
        for changed in variants:
            with self.subTest(changed=changed[-80:]):
                self.checksum.write_text(changed)
                with self.assertRaises(ValueError):
                    release.verify_checksums(self.checksum, self.receipt, self.root, registry=True)
        self.checksum.write_text(valid)
        release.verify_checksums(self.checksum, self.receipt, self.root, registry=True)

    def test_full_closure_rejects_unknown_missing_and_tampered_companion(self):
        self.sign()
        extra = self.root / 'unknown.json'
        extra.write_text('{}')
        with self.assertRaisesRegex(ValueError, 'asset set'):
            signer.verify_provider_release(self.root, self.fingerprint, {'version': '2.1.0-rc.2'})
        extra.unlink()
        companion = self.root / next(n for n in self.receipt['files'] if n.endswith('.provenance.json'))
        original = companion.read_bytes()
        companion.unlink()
        with self.assertRaisesRegex(ValueError, 'asset set'):
            signer.verify_provider_release(self.root, self.fingerprint, {'version': '2.1.0-rc.2'})
        companion.write_bytes(original + b'tampered')
        with self.assertRaisesRegex(ValueError, 'payload bytes'):
            signer.verify_provider_release(self.root, self.fingerprint, {'version': '2.1.0-rc.2'})

    def test_wrong_key_cross_bound_signature_and_receipt_tamper_refused(self):
        self.sign()
        with self.assertRaisesRegex(ValueError, 'expected key'):
            signer.verify_provider_release(self.root, '0' * 40, {'version': '2.1.0-rc.2'})
        receipt = self.root / 'release-content.json'
        with self.assertRaises((ValueError, subprocess.SubprocessError)):
            signer.verify_gpg(receipt, self.root / (self.checksum.name + '.sig'), self.fingerprint)
        receipt.write_bytes(receipt.read_bytes() + b' ')
        with self.assertRaises((ValueError, subprocess.SubprocessError)):
            signer.verify_provider_release(self.root, self.fingerprint, {'version': '2.1.0-rc.2'})

    def test_consumer_requires_independent_frozen_producer(self):
        self.sign()
        with self.assertRaisesRegex(ValueError, 'frozen source'):
            signer.verify_provider_release(self.root, self.fingerprint, {'version': '2.1.0-rc.1'})

    def test_cannot_overwrite_existing_signature(self):
        self.sign()
        signature = self.root / (self.checksum.name + '.sig')
        before = signature.read_bytes()
        with self.assertRaisesRegex(ValueError, 'new adjacent'):
            signer.sign_gpg(self.checksum, signature, self.fingerprint)
        self.assertEqual(signature.read_bytes(), before)

    def test_concatenated_valid_signatures_are_not_one_expected_signature(self):
        self.sign()
        receipt = self.root / 'release-content.json'
        signature = self.root / 'release-content.json.sig'
        original = signature.read_bytes()
        signature.write_bytes(original + original)
        with self.assertRaisesRegex(ValueError, 'expected key'):
            signer.verify_gpg(receipt, signature, self.fingerprint)

    def test_canonical_mutation_after_receipt_signing_is_refused(self):
        self.sign()
        self.checksum.write_text(self.checksum.read_text() + '\n')
        with self.assertRaises((ValueError, subprocess.SubprocessError)):
            signer.verify_provider_release(self.root, self.fingerprint, {'version': '2.1.0-rc.2'})

    def test_signed_receipt_cannot_drop_target_or_add_unrecognized_subject(self):
        for mutation in ('missing', 'unknown', 'manifest-alias'):
            with self.subTest(mutation=mutation):
                changed = json.loads(json.dumps(self.receipt))
                if mutation == 'missing':
                    changed['files'].pop(next(n for n in changed['files'] if n.endswith('.zip')))
                elif mutation == 'unknown':
                    changed['files']['unknown.json'] = '0' * 64
                else:
                    changed['files'][self.prefix + '_manifest.json'] = changed['files']['terraform-registry-manifest.json']
                with self.assertRaisesRegex(ValueError, 'exactly thirteen'):
                    release.verify_published_content(self.root, changed)

    def test_even_authentic_signature_cannot_accept_duplicate_receipt_keys(self):
        self.sign()
        receipt = self.root / 'release-content.json'
        receipt.write_text('{"source":{"version":"wrong"},' + receipt.read_text()[1:])
        signature = self.root / 'release-content.json.sig'
        signature.unlink()
        signer.sign_gpg(receipt, signature, self.fingerprint)
        with self.assertRaisesRegex(ValueError, 'duplicate qualified receipt key'):
            signer.verify_provider_release(self.root, self.fingerprint, {'version': '2.1.0-rc.2'})


class GenerationRecipeTests(unittest.TestCase):
    def exercise_generation(self, fail_once):
        with tempfile.TemporaryDirectory(prefix="provider-generation-") as temporary:
            root = Path(temporary)
            tools = root / "tools"
            tools.mkdir()
            version = re.search(r"(?m)^go ([0-9.]+)$", (release.ROOT / "go.mod").read_text()).group(1)
            toolchain = re.search(r"(?m)^release_go_version=(go[0-9.]+)$", (release.ROOT / "scripts/release-tools.sh").read_text()).group(1)
            (root / "go.mod").write_text("module example.com/generation-fixture\n\ngo " + version + "\n")
            (tools / "go.mod").write_text("module example.com/generation-tools\n\ngo " + version + "\n")
            (root / "generator.go").write_text("//go:generate sh generate.sh\npackage fixture\n")
            (tools / "generator.go").write_text("//go:generate touch second-phase-executed\npackage tools\n")
            script = "touch first-phase-executed\n"
            if fail_once:
                script = "if [ ! -f first-attempt ]; then touch first-attempt; exit 7; fi\ntouch unexpected-root-retry\n"
            (root / "generate.sh").write_text(script)
            env = dict(os.environ, GOTOOLCHAIN=toolchain, GOWORK="off", GOENV="off", GOFLAGS="", GOEXPERIMENT="")
            result = subprocess.run(["make", "-f", str(release.ROOT / "GNUmakefile"), "generate"],
                                    cwd=root, env=env, capture_output=True, text=True, timeout=30)
            if fail_once:
                self.assertNotEqual(result.returncode, 0, "the first Go generation failure must reach Make")
                self.assertFalse((root / "unexpected-root-retry").exists(), "failed generation must not run again implicitly")
                self.assertFalse((tools / "second-phase-executed").exists())
            else:
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertTrue((root / "first-phase-executed").exists())
                self.assertTrue((tools / "second-phase-executed").exists())

    def test_real_generation_failure_stops_before_an_implicit_retry(self):
        self.exercise_generation(fail_once=True)

    def test_real_generation_runs_both_modules_on_success(self):
        self.exercise_generation(fail_once=False)


if __name__ == '__main__':
    unittest.main()
