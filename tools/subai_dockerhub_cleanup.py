"""Remove obsolete tags from this project's Docker Hub repository only.

Defaults to a read-only plan. Deletion requires --delete and a verified current
multi-platform image under both latest and the current commit tag.
"""

import argparse
import json
import os
import re
import urllib.parse
import urllib.request

BASE = "https://hub.docker.com"
REPOSITORY = "/v2/repositories/allen0039/subai-server/tags/"


def request(path, token=None, method="GET", body=None):
    url = urllib.parse.urljoin(BASE, path)
    parsed = urllib.parse.urlparse(url)
    if parsed.scheme != "https" or parsed.netloc != "hub.docker.com":
        raise ValueError("Unexpected Docker Hub API host")
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "JWT " + token
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    with urllib.request.urlopen(req, timeout=60) as response:
        data = response.read()
        return json.loads(data) if data else None


def cleanup(delete=False):
    sha = os.environ["GITHUB_SHA"]
    if not re.fullmatch(r"[0-9a-f]{40}", sha):
        raise ValueError("Expected full commit SHA")
    keep = {"latest", "sha-" + sha}
    token = None
    if delete:
        token = request("/v2/users/login/", method="POST", body={
            "username": os.environ["DOCKERHUB_USERNAME"],
            "password": os.environ["DOCKERHUB_TOKEN"],
        })["token"]
    tags = {}
    page = REPOSITORY + "?page_size=100"
    while page:
        result = request(page, token)
        tags.update((tag["name"], tag) for tag in result["results"])
        page = result.get("next")
    if not keep.issubset(tags):
        raise RuntimeError("New image tags are missing; refusing cleanup")
    current = tags["sha-" + sha]
    digest = current.get("digest")
    if not digest or tags["latest"].get("digest") != digest:
        raise RuntimeError("Latest does not match current commit; refusing cleanup")
    platforms = {(i.get("os"), i.get("architecture")) for i in current["images"]}
    if not {("linux", "amd64"), ("linux", "arm64")}.issubset(platforms):
        raise RuntimeError("Current image lacks required platforms; refusing cleanup")
    obsolete = sorted(set(tags) - keep)
    print(json.dumps({"keep": sorted(keep), "delete": obsolete, "dry_run": not delete}))
    if delete:
        for name in obsolete:
            request(REPOSITORY + urllib.parse.quote(name, safe="") + "/", token, "DELETE")
            print("Deleted tag: " + name)
    return obsolete


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--delete", action="store_true")
    cleanup(parser.parse_args().delete)
