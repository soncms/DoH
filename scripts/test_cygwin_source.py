"""Adversarial fixtures for the read-only native-source verifier."""

import hashlib
import io
import tarfile
import unittest

from verify_cygwin_source import compare_source, read_member


class SourceVerificationTests(unittest.TestCase):
    def setUp(self):
        self.content = b"verified source\n"
        self.spec = {"sourceRoot": "source", "sourceCommit": "commit",
                     "sourceFiles": 1, "omittedGitMetadata": []}
        blob = hashlib.sha1(b"blob 16\0" + self.content).hexdigest()
        self.tree = {"sha": "commit", "truncated": False, "tree": [
            {"path": "code.c", "type": "blob", "mode": "100644", "sha": blob}]}

    def archive(self, members):
        data = io.BytesIO()
        with tarfile.open(fileobj=data, mode="w:bz2") as archive:
            for name, content, mode, kind in members:
                member = tarfile.TarInfo(name)
                member.size = len(content)
                member.mode = mode
                member.type = kind
                archive.addfile(member, io.BytesIO(content))
        return data.getvalue()

    def member(self, name="source/code.c", content=None, mode=0o644, kind=tarfile.REGTYPE):
        return name, self.content if content is None else content, mode, kind

    def test_valid_source(self):
        self.assertEqual(compare_source(self.archive([self.member()]), self.spec, self.tree), 1)

    def test_changed_blob_or_executable_mode(self):
        for member in [self.member(content=b"changed"), self.member(mode=0o755)]:
            with self.subTest(member=member), self.assertRaisesRegex(ValueError, "blob/mode"):
                compare_source(self.archive([member]), self.spec, self.tree)

    def test_duplicate_path(self):
        with self.assertRaisesRegex(ValueError, "Duplicate"):
            compare_source(self.archive([self.member(), self.member()]), self.spec, self.tree)

    def test_unsafe_paths(self):
        for name in ["source/../code.c", "/source/code.c", "source\\code.c", "source/code.c:stream"]:
            with self.subTest(name=name), self.assertRaisesRegex(ValueError, "Unsafe"):
                compare_source(self.archive([self.member(name=name)]), self.spec, self.tree)

    def test_extra_or_missing_source(self):
        for members in [[], [self.member(name="source/extra.c")]]:
            with self.subTest(members=members), self.assertRaises(ValueError):
                compare_source(self.archive(members), self.spec, self.tree)

    def test_incomplete_or_wrong_commit(self):
        for key, value in [("truncated", True), ("sha", "another commit")]:
            tree = dict(self.tree, **{key: value})
            with self.subTest(key=key), self.assertRaisesRegex(ValueError, "incomplete"):
                compare_source(self.archive([self.member()]), self.spec, tree)

    def test_hardlink_rejected(self):
        with self.assertRaisesRegex(ValueError, "Unsupported"):
            compare_source(self.archive([self.member(kind=tarfile.LNKTYPE)]), self.spec, self.tree)

    def test_reference_member_must_be_unique_and_regular(self):
        for members in [[self.member(), self.member()], [self.member(kind=tarfile.LNKTYPE)]]:
            with self.subTest(members=members):
                with tarfile.open(fileobj=io.BytesIO(self.archive(members))) as archive:
                    with self.assertRaises(ValueError):
                        read_member(archive, "source/code.c", 1024)


if __name__ == "__main__":
    unittest.main()
