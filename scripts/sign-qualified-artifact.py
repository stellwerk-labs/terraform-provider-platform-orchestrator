#!/usr/bin/env python3
"""Recheck qualified bytes before invoking the existing release signer."""
import os
from pathlib import Path
import re
import subprocess
import sys
import shutil
import tempfile

import qualify_release as release


def verify_gpg(artifact, signature, fingerprint):
    for path in (artifact, signature):
        if path.is_symlink() or not path.is_file() or not path.stat().st_size:
            raise ValueError("signature verification requires exact regular evidence")
    status = release.checked(["gpg", "--status-fd", "1", "--verify", str(signature), str(artifact)]).decode()
    records = [line.split() for line in status.splitlines() if line.startswith("[GNUPG:] ")]
    invalid = {"BADSIG", "ERRSIG", "EXPSIG", "EXPKEYSIG", "REVKEYSIG", "KEYREVOKED", "KEYEXPIRED", "SIGEXPIRED", "NO_PUBKEY"}
    valid = [record for record in records if record[1] == "VALIDSIG"]
    if (any(record[1] in invalid for record in records) or len(valid) != 1
            or fingerprint.upper() not in (valid[0][2], valid[0][-1])):
        raise ValueError("Registry signature does not bind the expected key")


def sign_gpg(artifact, signature, fingerprint):
    if not re.fullmatch(r"[A-Fa-f0-9]{40,64}", fingerprint):
        raise ValueError("exact Registry signing fingerprint required")
    if signature.parent != artifact.parent or signature.exists() or signature.is_symlink():
        raise ValueError("signature must be a new adjacent artifact")
    # Passphrase is stdin-only, never argv or log output.
    subprocess.run(["gpg", "--batch", "--pinentry-mode", "loopback", "--passphrase-fd", "0",
                    "--local-user", fingerprint, "--output", str(signature), "--detach-sign", str(artifact)],
                   input=os.environ["GPG_PASSPHRASE"].encode() + b"\n", check=True)
    verify_gpg(artifact, signature, fingerprint)


def verify_provider_release(directory, fingerprint, expected_source):
    """Authenticate both contracts before trusting the published payload graph."""
    directory = Path(directory)
    receipt = directory / "release-content.json"
    verify_gpg(receipt, directory / "release-content.json.sig", fingerprint)
    content = release.read_receipt(receipt)
    if content["source"] != expected_source:
        raise ValueError("published producer identity differs from the frozen source")
    checksum = directory / ("terraform-provider-platform-orchestrator_" + content["source"]["version"] + "_SHA256SUMS")
    verify_gpg(checksum, directory / (checksum.name + ".sig"), fingerprint)
    release.verify_published_content(directory, content)
    return content


def main():
    mode, source, tag, version, artifact, signature, *remaining = sys.argv[1:]
    os.environ.update(QUALIFY_SOURCE_SHA=source, QUALIFY_TAG=tag, QUALIFY_VERSION=version)
    producer = release.identity()
    expected = "stellwerk-labs/" + ("platform-orchestrator-cli" if mode == "cosign" else "terraform-provider-platform-orchestrator")
    if os.environ.get("GITHUB_ACTIONS") != "true" or producer["repository"] != expected:
        raise ValueError("release signing requires the existing public GitHub workflow authority")
    artifact, signature = Path(artifact).resolve(), Path(signature).resolve()
    shipping_evidence = [signature]
    if signature.parent != artifact.parent or signature.exists():
        raise ValueError("signature must be a new adjacent artifact")
    release.recheck(artifact.parent, artifact)
    if mode == "cosign":
        certificate, = remaining
        certificate = Path(certificate).resolve()
        shipping_evidence.append(certificate)
        if certificate.parent != artifact.parent or certificate.exists():
            raise ValueError("certificate must be a new adjacent artifact")
        subprocess.run(["cosign", "sign-blob", "--oidc-provider=github-actions",
                        "--output-certificate=" + str(certificate), "--output-signature=" + str(signature),
                        str(artifact), "--yes"], check=True)
        subprocess.run(["cosign", "verify-blob", "--certificate", str(certificate), "--signature", str(signature),
                        "--certificate-identity", "https://github.com/" + producer["workflowRef"],
                        "--certificate-oidc-issuer", "https://token.actions.githubusercontent.com", str(artifact)], check=True)
    elif mode == "gpg":
        fingerprint, = remaining
        expected_checksum = "terraform-provider-platform-orchestrator_" + version + "_SHA256SUMS"
        if artifact.name != expected_checksum or signature.name != expected_checksum + ".sig":
            raise ValueError("provider signer requires the canonical Registry checksum")
        receipt = artifact.parent / "release-content.json"
        receipt_signature = artifact.parent / "release-content.json.sig"
        if receipt_signature.exists() or receipt_signature.is_symlink():
            raise ValueError("receipt signature must be a new adjacent artifact")
        sign_gpg(artifact, signature, fingerprint)
        # Retain every archive/SPDX/provenance subject under a separately signed
        # full receipt; never weaken the qualified payload recheck.
        release.recheck(artifact.parent, receipt)
        sign_gpg(receipt, receipt_signature, fingerprint)
        shipping_evidence.append(receipt_signature)
        # End the complete dual-signing interval on the same qualified bytes,
        # not merely the canonical checksum's earlier successful signature.
        release.recheck(artifact.parent, artifact)
        release.recheck(artifact.parent, receipt)
        verify_gpg(artifact, signature, fingerprint)
        verify_gpg(receipt, receipt_signature, fingerprint)
    else:
        raise ValueError("unsupported release signer")
    with tempfile.TemporaryDirectory(prefix="client-signature-scan-") as temporary:
        for path in shipping_evidence:
            if path.is_symlink() or not path.is_file() or not path.stat().st_size:
                raise ValueError("signer did not produce exact regular evidence")
            shutil.copyfile(path, Path(temporary) / path.name)
        release.checked([str(release.ROOT / "scripts/scan-release-content.sh"), temporary, "jayonthenet"])


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, KeyError, TypeError, subprocess.SubprocessError):
        print("qualified release signing failed", file=sys.stderr)
        sys.exit(1)
