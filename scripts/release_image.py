#!/usr/bin/env python3
"""Validate release refs and advance Docker Hub tags without a version rollback."""

import argparse
import json
import os
import re
import subprocess
import sys
from dataclasses import dataclass


TAG_RE = re.compile(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\Z")
VERSION_RE = re.compile(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\Z")
SHA_RE = re.compile(r"[0-9a-f]{40}\Z")
DIGEST_RE = re.compile(r"sha256:[0-9a-f]{64}\Z")
USERNAME_RE = re.compile(r"[a-z0-9][a-z0-9_-]*\Z")
INDEX_TYPES = {
    "application/vnd.oci.image.index.v1+json",
    "application/vnd.docker.distribution.manifest.list.v2+json",
}
VERSION_ANNOTATION = "org.opencontainers.image.version"
REVISION_ANNOTATION = "org.opencontainers.image.revision"


class ReleaseError(Exception):
    pass


@dataclass(frozen=True)
class PublishedImage:
    version: str
    revision: str


def version_tuple(value):
    match = VERSION_RE.fullmatch(value) if isinstance(value, str) else None
    if not match:
        raise ReleaseError("image has an invalid stable version annotation")
    return tuple(int(part) for part in match.groups())


def release_version(tag):
    match = TAG_RE.fullmatch(tag)
    if not match:
        raise ReleaseError("release tag must be vMAJOR.MINOR.PATCH without leading zeros or suffixes")
    return ".".join(match.groups())


def command(*args):
    return subprocess.run(args, text=True, capture_output=True, check=False)


def required_env(name):
    value = os.environ.get(name, "")
    if not value:
        raise ReleaseError(f"{name} is required")
    return value


def git_output(*args):
    result = command("git", *args)
    if result.returncode:
        raise ReleaseError(f"git {' '.join(args[:2])} failed")
    return result.stdout.strip()


def validate_release():
    if os.environ.get("GITHUB_EVENT_NAME") != "push" or os.environ.get("GITHUB_REF_TYPE") != "tag":
        raise ReleaseError("release requires a tag push event")
    tag = required_env("GITHUB_REF_NAME")
    version = release_version(tag)
    commit = git_output("rev-parse", "--verify", f"refs/tags/{tag}^{{commit}}")
    if not SHA_RE.fullmatch(commit) or git_output("rev-parse", "HEAD") != commit:
        raise ReleaseError("checked-out commit does not match the release tag")
    expected = os.environ.get("EXPECTED_RELEASE_COMMIT", "")
    if expected and expected != commit:
        raise ReleaseError("release tag changed between verification and publication")
    main = git_output("rev-parse", "--verify", "refs/remotes/origin/main")
    if not SHA_RE.fullmatch(main):
        raise ReleaseError("origin/main is unavailable")
    if command("git", "merge-base", "--is-ancestor", commit, main).returncode:
        raise ReleaseError("release commit is not in origin/main history")
    output = os.environ.get("GITHUB_OUTPUT")
    if output:
        with open(output, "a", encoding="utf-8") as stream:
            stream.write(f"version={version}\ncommit={commit}\n")
    print(f"Validated release v{version} on origin/main")


def check_credentials():
    username = required_env("DOCKERHUB_USERNAME")
    required_env("DOCKERHUB_TOKEN")
    if not USERNAME_RE.fullmatch(username):
        raise ReleaseError("DOCKERHUB_USERNAME must be a lowercase Docker Hub namespace")
    print("Docker Hub credentials are configured")


def release_inputs():
    version = required_env("RELEASE_VERSION")
    version_tuple(version)
    commit = required_env("RELEASE_COMMIT")
    if not SHA_RE.fullmatch(commit):
        raise ReleaseError("RELEASE_COMMIT is invalid")
    image = required_env("IMAGE_NAME")
    username = image.removeprefix("docker.io/").removesuffix("/filehola")
    if image != f"docker.io/{username}/filehola" or not USERNAME_RE.fullmatch(username):
        raise ReleaseError("IMAGE_NAME must be a Docker Hub filehola image")
    return version, commit, image


def inspect_image(image, tag):
    result = command("docker", "buildx", "imagetools", "inspect", "--raw", f"{image}:{tag}")
    if result.returncode:
        error = result.stderr.lower()
        if ": not found" in error or "manifest unknown" in error:
            return None
        raise ReleaseError(f"cannot inspect Docker Hub tag {tag}; refusing to overwrite it")
    try:
        manifest = json.loads(result.stdout)
        annotations = manifest["annotations"]
        version = annotations[VERSION_ANNOTATION]
        revision = annotations[REVISION_ANNOTATION]
    except (ValueError, KeyError, TypeError) as exc:
        raise ReleaseError(f"Docker Hub tag {tag} lacks required OCI annotations") from exc
    if manifest.get("mediaType") not in INDEX_TYPES:
        raise ReleaseError(f"Docker Hub tag {tag} is not a multi-platform index")
    version_tuple(version)
    if not isinstance(revision, str) or not SHA_RE.fullmatch(revision):
        raise ReleaseError(f"Docker Hub tag {tag} has an invalid revision annotation")
    return PublishedImage(version, revision)


def preflight():
    version, commit, image = release_inputs()
    existing = inspect_image(image, version)
    if existing is None:
        publish = "true"
        print(f"Immutable tag {version} is available")
    elif existing == PublishedImage(version, commit):
        publish = "false"
        print(f"Immutable tag {version} already belongs to this commit; reusing it")
    else:
        raise ReleaseError(f"immutable tag {version} already belongs to another image")
    output = os.environ.get("GITHUB_OUTPUT")
    if output:
        with open(output, "a", encoding="utf-8") as stream:
            stream.write(f"publish={publish}\n")


def should_promote(target, incoming, existing):
    if existing is None:
        return True
    current = version_tuple(existing.version)
    candidate = version_tuple(incoming.version)
    if target != "latest" and current[:2] != candidate[:2]:
        raise ReleaseError(f"minor tag {target} points to another release line")
    if current > candidate:
        return False
    if current == candidate:
        if existing.revision != incoming.revision:
            raise ReleaseError(f"tag {target} has the same version but another revision")
        return False
    return True


def source_digest(image, version):
    result = command("docker", "buildx", "imagetools", "inspect", f"{image}:{version}")
    match = re.search(r"^Digest:[ \t]*(sha256:[0-9a-f]{64})[ \t]*$", result.stdout, re.MULTILINE)
    if result.returncode or match is None or not DIGEST_RE.fullmatch(match.group(1)):
        raise ReleaseError("cannot resolve immutable image digest")
    return match.group(1)


def promote():
    version, commit, image = release_inputs()
    incoming = PublishedImage(version, commit)
    if inspect_image(image, version) != incoming:
        raise ReleaseError("immutable image is missing or does not match the release commit")
    digest = source_digest(image, version)
    major, minor, _ = version_tuple(version)
    for tag in (f"{major}.{minor}", "latest"):
        existing = inspect_image(image, tag)
        if not should_promote(tag, incoming, existing):
            print(f"Keeping {tag}; it already points to this or a newer release")
            continue
        result = command(
            "docker", "buildx", "imagetools", "create",
            "--annotation", f"index:{VERSION_ANNOTATION}={version}",
            "--annotation", f"index:{REVISION_ANNOTATION}={commit}",
            "--tag", f"{image}:{tag}", f"{image}@{digest}",
        )
        if result.returncode or inspect_image(image, tag) != incoming:
            raise ReleaseError(f"failed to publish or verify floating tag {tag}")
        print(f"Advanced {tag} to {version}")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("action", choices=("validate", "credentials", "preflight", "promote"))
    action = parser.parse_args().action
    try:
        {"validate": validate_release, "credentials": check_credentials,
         "preflight": preflight, "promote": promote}[action]()
    except ReleaseError as exc:
        print(f"Release blocked: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
