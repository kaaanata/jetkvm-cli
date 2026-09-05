#!/usr/bin/env python3
"""Emit an explicit SBOM for the embedded C codec and its source archive."""
import hashlib
import json
import pathlib
import sys

root = pathlib.Path(__file__).resolve().parent.parent
source = root / "internal/video/wasmdecoder/ffmpeg-9.0.1.tar.xz"
sha = hashlib.sha256(source.read_bytes()).hexdigest()
output = pathlib.Path(sys.argv[1])
output.parent.mkdir(parents=True, exist_ok=True)
doc = {
    "spdxVersion": "SPDX-2.3", "dataLicense": "CC0-1.0", "SPDXID": "SPDXRef-DOCUMENT",
    "name": "jetkvm-embedded-h264-decoder",
    "documentNamespace": "https://github.com/kaaanata/jetkvm-cli/decoder/" + sha,
    "creationInfo": {"created": "2026-09-05T00:00:00Z", "creators": ["Tool: jetkvm-decoder-metadata"]},
    "packages": [{
        "SPDXID": "SPDXRef-ffmpeg", "name": "ffmpeg", "versionInfo": "9.0.1",
        "downloadLocation": "https://ffmpeg.org/releases/ffmpeg-9.0.1.tar.xz",
        "filesAnalyzed": False, "licenseConcluded": "LGPL-2.1-or-later", "licenseDeclared": "LGPL-2.1-or-later",
        "copyrightText": "NOASSERTION", "checksums": [{"algorithm": "SHA256", "checksumValue": sha}],
        "externalRefs": [
            {"referenceCategory": "SECURITY", "referenceType": "cpe23Type", "referenceLocator": "cpe:2.3:a:ffmpeg:ffmpeg:9.0.1:*:*:*:*:*:*:*"},
            {"referenceCategory": "PACKAGE-MANAGER", "referenceType": "purl", "referenceLocator": "pkg:generic/ffmpeg@9.0.1"}
        ],
        "comment": "Only libavcodec H.264 decoding and libavutil are enabled. No encoders, network, GPL, or nonfree components."
    }],
    "relationships": [{"spdxElementId": "SPDXRef-DOCUMENT", "relationshipType": "DESCRIBES", "relatedSpdxElement": "SPDXRef-ffmpeg"}]
}
output.write_text(json.dumps(doc, indent=2) + "\n")
