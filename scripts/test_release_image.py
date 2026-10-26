import os
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import release_image as release


COMMIT = "a" * 40
OTHER = "b" * 40
IMAGE = "docker.io/example/filehola"


class ReleaseImageTests(unittest.TestCase):
    def test_strict_stable_tag(self):
        self.assertEqual(release.release_version("v0.1.0"), "0.1.0")
        self.assertEqual(release.release_version("v10.20.30"), "10.20.30")
        for tag in ("v01.1.0", "v1.02.3", "v1.2.3-rc1", "v1.2.3+build", "1.2.3", "v1.2", "v1.2.3.4"):
            with self.subTest(tag=tag), self.assertRaises(release.ReleaseError):
                release.release_version(tag)

    def test_only_tag_push_can_validate(self):
        for event, ref_type in (("pull_request", "tag"), ("push", "branch")):
            with self.subTest(event=event, ref_type=ref_type):
                with mock.patch.dict(os.environ, {"GITHUB_EVENT_NAME": event, "GITHUB_REF_TYPE": ref_type, "GITHUB_REF_NAME": "v0.1.0"}):
                    with self.assertRaises(release.ReleaseError):
                        release.validate_release()

    def test_commit_must_belong_to_main_and_match_tag(self):
        env = {"GITHUB_EVENT_NAME": "push", "GITHUB_REF_TYPE": "tag", "GITHUB_REF_NAME": "v0.1.0"}
        with mock.patch.dict(os.environ, env, clear=True):
            with mock.patch.object(release, "git_output", side_effect=[COMMIT, COMMIT, OTHER]):
                with mock.patch.object(release, "command", return_value=subprocess.CompletedProcess([], 1)):
                    with self.assertRaisesRegex(release.ReleaseError, "not in origin/main"):
                        release.validate_release()
            with mock.patch.object(release, "git_output", side_effect=[COMMIT, OTHER]):
                with self.assertRaisesRegex(release.ReleaseError, "does not match"):
                    release.validate_release()

    def test_real_git_ancestry_check(self):
        with tempfile.TemporaryDirectory() as directory:
            def git(*args):
                subprocess.run(("git", *args), cwd=directory, check=True, capture_output=True)

            git("init", "-q", "-b", "main")
            git("config", "user.name", "Release Test")
            git("config", "user.email", "release-test@example.invalid")
            Path(directory, "README").write_text("main\n", encoding="utf-8")
            git("add", "README")
            git("commit", "-q", "-m", "main")
            git("tag", "v0.1.0")
            git("update-ref", "refs/remotes/origin/main", "HEAD")
            previous = os.getcwd()
            try:
                os.chdir(directory)
                with mock.patch.dict(os.environ, {"GITHUB_EVENT_NAME": "push", "GITHUB_REF_TYPE": "tag", "GITHUB_REF_NAME": "v0.1.0"}, clear=True):
                    release.validate_release()
                git("checkout", "-q", "--orphan", "unmerged")
                Path(directory, "README").write_text("unmerged\n", encoding="utf-8")
                git("add", "README")
                git("commit", "-q", "-m", "unmerged")
                git("tag", "v0.2.0")
                with mock.patch.dict(os.environ, {"GITHUB_EVENT_NAME": "push", "GITHUB_REF_TYPE": "tag", "GITHUB_REF_NAME": "v0.2.0"}, clear=True):
                    with self.assertRaisesRegex(release.ReleaseError, "not in origin/main"):
                        release.validate_release()
            finally:
                os.chdir(previous)

    def test_missing_credentials_block_publication(self):
        with mock.patch.dict(os.environ, {}, clear=True):
            with self.assertRaisesRegex(release.ReleaseError, "DOCKERHUB_USERNAME"):
                release.check_credentials()
        with mock.patch.dict(os.environ, {"DOCKERHUB_USERNAME": "example"}, clear=True):
            with self.assertRaisesRegex(release.ReleaseError, "DOCKERHUB_TOKEN"):
                release.check_credentials()

    def test_unknown_registry_failure_is_not_treated_as_missing_tag(self):
        with mock.patch.object(release, "command", return_value=subprocess.CompletedProcess([], 1, "", "unauthorized")):
            with self.assertRaisesRegex(release.ReleaseError, "refusing to overwrite"):
                release.inspect_image(IMAGE, "latest")
        with mock.patch.object(release, "command", return_value=subprocess.CompletedProcess([], 1, "", "ERROR: tag: not found")):
            self.assertIsNone(release.inspect_image(IMAGE, "latest"))

    def test_existing_immutable_tag_is_never_rebuilt(self):
        env = {"IMAGE_NAME": IMAGE, "RELEASE_VERSION": "0.1.0", "RELEASE_COMMIT": COMMIT}
        with mock.patch.dict(os.environ, env, clear=True):
            with mock.patch.object(release, "inspect_image", return_value=release.PublishedImage("0.1.0", COMMIT)):
                release.preflight()
            with mock.patch.object(release, "inspect_image", return_value=release.PublishedImage("0.1.0", OTHER)):
                with self.assertRaisesRegex(release.ReleaseError, "already belongs"):
                    release.preflight()

    def test_floating_tags_only_advance(self):
        incoming = release.PublishedImage("0.1.1", COMMIT)
        self.assertTrue(release.should_promote("0.1", incoming, None))
        self.assertTrue(release.should_promote("latest", incoming, release.PublishedImage("0.1.0", OTHER)))
        self.assertFalse(release.should_promote("0.1", incoming, release.PublishedImage("0.1.2", OTHER)))
        self.assertFalse(release.should_promote("latest", incoming, release.PublishedImage("0.2.0", OTHER)))
        self.assertFalse(release.should_promote("latest", incoming, incoming))
        with self.assertRaisesRegex(release.ReleaseError, "another revision"):
            release.should_promote("latest", incoming, release.PublishedImage("0.1.1", OTHER))
        with self.assertRaisesRegex(release.ReleaseError, "another release line"):
            release.should_promote("0.1", incoming, release.PublishedImage("0.2.0", OTHER))

    def test_source_digest_uses_top_level_manifest_digest(self):
        digest = "sha256:" + "c" * 64
        output = f"Name: {IMAGE}:0.1.0\nMediaType: application/vnd.oci.image.index.v1+json\nDigest:    {digest}\nManifests:\n"
        with mock.patch.object(release, "command", return_value=subprocess.CompletedProcess([], 0, output, "")):
            self.assertEqual(release.source_digest(IMAGE, "0.1.0"), digest)
        with mock.patch.object(release, "command", return_value=subprocess.CompletedProcess([], 0, "not a manifest", "")):
            with self.assertRaisesRegex(release.ReleaseError, "cannot resolve"):
                release.source_digest(IMAGE, "0.1.0")

    def test_old_rerun_does_not_push_floating_tags(self):
        env = {"IMAGE_NAME": IMAGE, "RELEASE_VERSION": "0.1.0", "RELEASE_COMMIT": COMMIT}
        manifests = [
            release.PublishedImage("0.1.0", COMMIT),
            release.PublishedImage("0.1.1", OTHER),
            release.PublishedImage("0.2.0", OTHER),
        ]
        with mock.patch.dict(os.environ, env, clear=True):
            with mock.patch.object(release, "inspect_image", side_effect=manifests):
                with mock.patch.object(release, "source_digest", return_value="sha256:" + "c" * 64):
                    with mock.patch.object(release, "command") as run:
                        release.promote()
                        run.assert_not_called()

    def test_first_release_creates_both_floating_tags_from_one_digest(self):
        env = {"IMAGE_NAME": IMAGE, "RELEASE_VERSION": "0.1.0", "RELEASE_COMMIT": COMMIT}
        incoming = release.PublishedImage("0.1.0", COMMIT)
        manifests = [incoming, None, incoming, None, incoming]
        digest = "sha256:" + "c" * 64
        with mock.patch.dict(os.environ, env, clear=True):
            with mock.patch.object(release, "inspect_image", side_effect=manifests):
                with mock.patch.object(release, "source_digest", return_value=digest):
                    with mock.patch.object(release, "command", return_value=subprocess.CompletedProcess([], 0)) as run:
                        release.promote()
        targets = []
        for called in run.call_args_list:
            args = called.args
            targets.append(args[args.index("--tag") + 1])
            self.assertEqual(args[-1], f"{IMAGE}@{digest}")
        self.assertEqual(targets, [f"{IMAGE}:0.1", f"{IMAGE}:latest"])


if __name__ == "__main__":
    unittest.main()
