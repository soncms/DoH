"""Check pinned Cygwin archives against runtime pins and an upstream Git tree.

Uses only Python's standard library. No archive is extracted, no downloaded
code is imported/executed, and symlinks are compared as Git blob bytes.
"""

import argparse
import hashlib
import io
import json
from pathlib import Path, PurePosixPath
import sys
import tarfile


def require(condition, message):
    if not condition:
        raise ValueError(message)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def safe_name(name):
    path = PurePosixPath(name)
    require(name and not path.is_absolute() and "\\" not in name
            and ":" not in name and ".." not in path.parts,
            f"Unsafe archive path: {name}")
    return path.as_posix()


def read_member(archive, name, maximum):
    matches = [m for m in archive if m.name == name]
    require(len(matches) == 1, f"Expected exactly one archive member: {name}")
    member = matches[0]
    require(member.isfile() and 0 <= member.size <= maximum,
            f"Invalid archive member type/size: {name}")
    with archive.extractfile(member) as stream:
        data = stream.read(maximum + 1)
    require(len(data) == member.size, f"Truncated archive member: {name}")
    return data


def pinned_archive(entries, component, kind, directory):
    matches = [e for e in entries if e["component"] == component and e["kind"] == kind]
    require(len(matches) == 1, f"Expected one {component} {kind} archive")
    entry = matches[0]
    require(PurePosixPath(entry["file"]).name == entry["file"]
            and "\\" not in entry["file"] and ":" not in entry["file"],
            "Invalid archive filename")
    path = directory / entry["file"]
    require(path.is_file() and path.stat().st_size <= 64 * 1024 * 1024,
            f"Missing/oversized archive: {path.name}")
    require(digest(path.read_bytes()) == entry["sha256"], f"Archive hash mismatch: {path.name}")
    return path


def compare_source(data, spec, tree):
    require(tree["sha"] == spec["sourceCommit"] and tree["truncated"] is False,
            "Upstream Git tree is incomplete or belongs to another commit")
    blobs = {}
    for entry in tree["tree"]:
        if entry["type"] == "blob":
            name = safe_name(entry["path"])
            require(name not in blobs, f"Duplicate Git tree path: {name}")
            blobs[name] = entry
    seen = set()
    source_files = set()
    total = 0
    with tarfile.open(fileobj=io.BytesIO(data), mode="r|bz2") as archive:
        for member in archive:
            name = safe_name(member.name)
            require(name not in seen, f"Duplicate source archive path: {name}")
            seen.add(name)
            parts = PurePosixPath(name).parts
            require(parts[0] == spec["sourceRoot"], f"Unexpected source root: {name}")
            if member.isdir():
                continue
            relative = PurePosixPath(*parts[1:]).as_posix()
            require(relative in blobs, f"Extra source file: {relative}")
            require(member.isfile() or member.issym(), f"Unsupported source member: {name}")
            require(0 <= member.size <= 32 * 1024 * 1024, f"Oversized source file: {name}")
            total += member.size
            require(total <= 512 * 1024 * 1024, "Source archive exceeds verification limit")
            if member.issym():
                content = member.linkname.encode("utf-8", "surrogateescape")
                mode = "120000"
            else:
                with archive.extractfile(member) as stream:
                    content = stream.read(member.size + 1)
                require(len(content) == member.size, f"Truncated source file: {name}")
                mode = "100755" if member.mode & 0o111 else "100644"
            blob = hashlib.sha1(b"blob " + str(len(content)).encode("ascii") + b"\0" + content).hexdigest()
            require(blob == blobs[relative]["sha"] and mode == blobs[relative]["mode"],
                    f"Source blob/mode differs from upstream: {relative}")
            source_files.add(relative)
    require(len(source_files) == spec["sourceFiles"], "Source file count differs from lock")
    require(set(blobs) - source_files == set(spec["omittedGitMetadata"]),
            "Missing upstream source files differ from documented Git metadata omissions")
    return len(source_files)


def verify(root, directory):
    lock = json.loads((root / "assets/native-source-lock.json").read_text(encoding="utf-8-sig"))
    require(lock["schemaVersion"] == 1, "Unsupported native source lock")
    spec = lock["cygwin"]
    pins = json.loads((root / "assets/runtime-sha256.json").read_text(encoding="utf-8-sig"))
    require(spec["binarySha256"] == pins["assets"]["third_party/zapret2/cygwin1.dll"],
            "Cygwin source lock differs from embedded runtime pin")
    reference = pinned_archive(lock["archives"], "cygwin", "reference-binary", directory)
    source = pinned_archive(lock["archives"], "cygwin", "source", directory)
    with tarfile.open(reference, mode="r:xz") as archive:
        binary = read_member(archive, spec["binaryMember"], 16 * 1024 * 1024)
    require(digest(binary) == spec["binarySha256"], "Reference Cygwin DLL differs from runtime")
    with tarfile.open(source, mode="r:xz") as archive:
        recipe = read_member(archive, spec["recipeMember"], 1024 * 1024)
        nested = read_member(archive, spec["sourceMember"], 32 * 1024 * 1024)
    require(digest(recipe) == spec["recipeSha256"], "Cygport recipe hash mismatch")
    require(digest(nested) == spec["sourceSha256"], "Nested Cygwin source archive hash mismatch")
    tree_path = safe_name(spec["upstreamTree"])
    tree_data = (root / tree_path).read_bytes()
    require(digest(tree_data) == spec["upstreamTreeSha256"], "Upstream Git tree evidence hash mismatch")
    count = compare_source(nested, spec, json.loads(tree_data))
    return {"component": "cygwin", "binaryMatchesRuntime": True,
            "sourceCommit": spec["sourceCommit"], "sourceFilesMatched": count,
            "historicalMetadataSignatureVerified": False, "rebuildTested": False}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parent.parent)
    parser.add_argument("--archives", type=Path, required=True)
    args = parser.parse_args()
    try:
        print(json.dumps(verify(args.root, args.archives), indent=2))
    except (OSError, ValueError, KeyError, TypeError, tarfile.TarError) as error:
        print(f"Cygwin verification failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
