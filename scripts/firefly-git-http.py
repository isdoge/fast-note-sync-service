#!/usr/bin/env python3
"""Firefly git-http-backend gateway (Python 3 standard library only).

A minimal, security-focused HTTP gateway that exposes one or more bare Git
repositories through the host's ``git http-backend``, bound to loopback only.
The Fast Note Sync container (``network_mode: host``) pushes to
``http://127.0.0.1:4323/blog.git``; ``git-receive-pack`` then runs the bare
repository's ``post-receive`` hook in the *host* context, so NAS-specific
services (systemd units, ``/opt/firefly``, ``runuser``) are available.

Features
  * HTTP BasicAuth checked in constant time (``hmac.compare_digest``) against a
    token file containing ``user:password`` on one line (``0600``).
  * ``/<repo>.git/...`` is confined to a whitelist of bare repos inside
    ``--root``; anything else is 404. No path traversal, no arbitrary project
    roots handed to git-http-backend.
  * Transparent streaming of request body, status line, headers and body, so
    ``post-receive`` sideband output reaches the client.
  * Optional ``POST /__republish`` runs a configured publish script (for
    retrying a build after the NAS settings centre confirms pending changes).

Usage
  python3 scripts/firefly-git-http.py \\
      --bind 127.0.0.1 --port 4323 \\
      --root /opt/git --token-file /opt/firefly/.git-http-token \\
      --repo blog.git \\
      --republish-script /opt/firefly/settings-publish-nas.sh

  # Local end-to-end testing (no root needed):
  python3 scripts/firefly-git-http.py --root /tmp/bare --repo demo.git \\
      --token-file /tmp/token --port 4323 --no-republish

Notes
  * Loopback-only by default; ``--bind`` values other than a loopback address
    require ``--allow-non-loopback`` as an explicit acknowledgement.
  * ``git http-backend`` is looked up in the usual locations and can be
    overridden with ``--git-http-backend``.
  * Every request is authenticated, including the republish endpoint.
"""

from __future__ import annotations

import argparse
import base64
import hmac
import http.server
import os
import re
import shutil
import subprocess
import sys
import threading
import urllib.parse

DEFAULT_PORT = 4323
DEFAULT_BIND = "127.0.0.1"
MAX_REPUBLISH_SECONDS = 900
GIT_HTTP_BACKEND_CANDIDATES = (
    "/usr/lib/git-core/git-http-backend",
    "/usr/libexec/git-core/git-http-backend",
    "/usr/local/libexec/git-core/git-http-backend",
)

REPO_NAME_RE = re.compile(r"^[A-Za-z0-9._-]+\.git$")


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Loopback git-http-backend gateway")
    parser.add_argument("--bind", default=DEFAULT_BIND, help="bind address (default 127.0.0.1)")
    parser.add_argument("--port", type=int, default=DEFAULT_PORT, help=f"bind port (default {DEFAULT_PORT})")
    parser.add_argument("--root", required=True, help="directory containing bare repositories")
    parser.add_argument(
        "--repo",
        action="append",
        default=[],
        help="whitelisted bare repository name, e.g. blog.git (repeatable)",
    )
    parser.add_argument("--token-file", required=True, help="file containing 'user:password' (0600)")
    parser.add_argument("--git-http-backend", default=None, help="path to git-http-backend")
    parser.add_argument("--republish-script", default=None, help="script run by POST /__republish")
    parser.add_argument("--republish-user", default=None, help="user to run the republish script as")
    parser.add_argument("--no-republish", action="store_true", help="disable POST /__republish entirely")
    parser.add_argument("--allow-non-loopback", action="store_true", help="explicitly allow a non-loopback bind")
    parser.add_argument("--quiet", action="store_true", help="suppress per-request logging")
    return parser.parse_args(argv)


def is_loopback(host: str) -> bool:
    return host in ("127.0.0.1", "::1", "localhost")


def find_git_http_backend(explicit: str | None) -> str:
    if explicit:
        if not os.access(explicit, os.X_OK):
            sys.exit(f"git-http-backend not executable: {explicit}")
        return explicit
    found = shutil.which("git-http-backend")
    if found:
        return found
    for candidate in GIT_HTTP_BACKEND_CANDIDATES:
        if os.access(candidate, os.X_OK):
            return candidate
    sys.exit("git-http-backend not found; pass --git-http-backend")


def load_credentials(path: str) -> tuple[str, str]:
    """Read 'user:password' from path. Refuses group/world-readable files."""
    st = os.stat(path)
    if st.st_mode & 0o077:
        sys.exit(f"refusing to use {path}: permissions must be 0600 (mode={oct(st.st_mode & 0o777)})")
    with open(path, "r", encoding="utf-8") as handle:
        line = handle.readline().strip()
    if ":" not in line:
        sys.exit(f"{path} must contain 'user:password' on the first line")
    user, password = line.split(":", 1)
    if not user or not password:
        sys.exit(f"{path} has an empty user or password")
    return user, password


class GatewayState:
    def __init__(self, args: argparse.Namespace, backend: str, credentials: tuple[str, str]) -> None:
        self.args = args
        self.backend = backend
        self.credentials = credentials
        self.root = os.path.realpath(args.root)
        self.repos = {}
        for name in args.repo:
            if not REPO_NAME_RE.match(name):
                sys.exit(f"invalid --repo name: {name!r}")
            full = os.path.realpath(os.path.join(self.root, name))
            if os.path.dirname(full) != self.root:
                sys.exit(f"--repo escapes --root: {name!r}")
            if not os.path.isdir(full):
                sys.exit(f"repository does not exist: {full}")
            self.repos[name] = full
        if not self.repos:
            # Default: every *.git directory directly under --root.
            for entry in sorted(os.listdir(self.root)):
                full = os.path.join(self.root, entry)
                if REPO_NAME_RE.match(entry) and os.path.isdir(full):
                    self.repos[entry] = os.path.realpath(full)
        if not self.repos:
            sys.exit(f"no bare repositories found under {self.root}")
        self.republish_lock = threading.Lock()


class GitHTTPHandler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "firefly-git-http"
    sys_version = ""
    state: GatewayState  # injected by make_server

    # ---- logging ---------------------------------------------------------
    def log_message(self, fmt: str, *args) -> None:  # noqa: A003
        if not self.state.args.quiet:
            sys.stderr.write("%s - %s\n" % (self.address_string(), fmt % args))

    # ---- auth ------------------------------------------------------------
    def _authenticated(self) -> bool:
        header = self.headers.get("Authorization", "")
        if not header.startswith("Basic "):
            return False
        try:
            decoded = base64.b64decode(header[6:], validate=True).decode("utf-8")
        except Exception:
            return False
        if ":" not in decoded:
            return False
        user, password = decoded.split(":", 1)
        expected_user, expected_password = self.state.credentials
        user_ok = hmac.compare_digest(user.encode("utf-8"), expected_user.encode("utf-8"))
        pass_ok = hmac.compare_digest(password.encode("utf-8"), expected_password.encode("utf-8"))
        return user_ok and pass_ok

    def _require_auth(self) -> bool:
        if self._authenticated():
            return True
        self.send_response(401)
        self.send_header("WWW-Authenticate", 'Basic realm="firefly-git"')
        self.send_header("Content-Length", "0")
        self.end_headers()
        return False

    # ---- routing ---------------------------------------------------------
    def do_GET(self) -> None:  # noqa: N802
        self._handle_git()

    def do_POST(self) -> None:  # noqa: N802
        parsed = urllib.parse.urlsplit(self.path)
        if parsed.path == "/__republish":
            self._handle_republish()
            return
        self._handle_git()

    def do_PUT(self) -> None:  # noqa: N802
        self._handle_git()

    def _resolve_repo(self, path: str):
        """Map '/<repo>.git/<rest>' to (repo_full_path, path_info). None if not whitelisted."""
        clean = urllib.parse.unquote(urllib.parse.urlsplit(path).path)
        if not clean.startswith("/"):
            return None
        segments = clean.lstrip("/").split("/", 1)
        repo_name = segments[0]
        rest = "/" + segments[1] if len(segments) > 1 else ""
        if repo_name not in self.state.repos:
            return None
        return self.state.repos[repo_name], rest

    def _handle_git(self) -> None:
        if not self._require_auth():
            return
        resolved = self._resolve_repo(self.path)
        if resolved is None:
            self._send_plain(404, "repository not found")
            return
        repo_path, path_info = resolved

        content_length = int(self.headers.get("Content-Length") or 0)
        if content_length < 0:
            self._send_plain(400, "invalid content length")
            return
        body = self.rfile.read(content_length) if content_length else b""

        env = {
            "GIT_PROJECT_ROOT": self.state.root,
            "GIT_HTTP_EXPORT_ALL": "1",
            "PATH_INFO": path_info,
            "REQUEST_METHOD": self.command,
            "QUERY_STRING": urllib.parse.urlsplit(self.path).query,
            "CONTENT_TYPE": self.headers.get("Content-Type", ""),
            "CONTENT_LENGTH": str(len(body)),
            "REMOTE_USER": self.state.credentials[0],
            "REMOTE_ADDR": self.client_address[0],
            "GIT_HTTP_MAX_REQUEST_BUFFER": "100M",
            "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
        }
        # Ensure the whitelisted repo resolves to the real path inside --root.
        env["PATH_INFO"] = "/" + os.path.basename(repo_path) + path_info

        try:
            proc = subprocess.run(
                [self.state.backend],
                input=body,
                env=env,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                timeout=MAX_REPUBLISH_SECONDS,
            )
        except subprocess.TimeoutExpired:
            self._send_plain(504, "git backend timed out")
            return
        except OSError as exc:
            self._send_plain(500, f"git backend failed: {exc}")
            return

        if proc.stderr:
            sys.stderr.write(proc.stderr.decode("utf-8", "replace"))

        out = proc.stdout
        header_end = out.find(b"\r\n\r\n")
        sep_len = 4
        if header_end < 0:
            header_end = out.find(b"\n\n")
            sep_len = 2
        if header_end < 0:
            self._send_plain(502, "git backend produced no headers")
            return

        status_line = "200 OK"
        headers: list[tuple[str, str]] = []
        for raw in out[:header_end].splitlines():
            line = raw.decode("latin-1").strip()
            if not line:
                continue
            if line.lower().startswith("status:"):
                status_line = line.split(":", 1)[1].strip()
                continue
            if ":" in line:
                key, value = line.split(":", 1)
                headers.append((key.strip(), value.strip()))

        try:
            status_code = int(status_line.split(" ", 1)[0])
        except ValueError:
            status_code = 200

        payload = out[header_end + sep_len:]
        self.send_response(status_code)
        for key, value in headers:
            if key.lower() in ("status", "content-length", "transfer-encoding", "connection"):
                continue
            self.send_header(key, value)
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(payload)
            self.wfile.flush()

    def _handle_republish(self) -> None:
        if not self._require_auth():
            return
        args = self.state.args
        if args.no_republish or not args.republish_script:
            self._send_plain(404, "republish disabled")
            return
        if not self.state.republish_lock.acquire(blocking=False):
            self._send_plain(409, "republish already running")
            return
        try:
            command = ["bash", args.republish_script]
            if args.republish_user:
                command = ["runuser", "-u", args.republish_user, "--", *command]
            proc = subprocess.run(
                command,
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                timeout=MAX_REPUBLISH_SECONDS,
            )
            output = proc.stdout.decode("utf-8", "replace")[-8192:]
            status = 200 if proc.returncode == 0 else 500
            body = (f"exit={proc.returncode}\n{output}").encode("utf-8")
            self.send_response(status)
            self.send_header("Content-Type", "text/plain; charset=utf-8")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            self.wfile.flush()
        except subprocess.TimeoutExpired:
            self._send_plain(504, "republish timed out")
        finally:
            self.state.republish_lock.release()

    def _send_plain(self, status: int, message: str) -> None:
        body = message.encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "text/plain; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def make_server(args: argparse.Namespace) -> http.server.ThreadingHTTPServer:
    if not is_loopback(args.bind) and not args.allow_non_loopback:
        sys.exit(f"refusing to bind {args.bind}: only loopback is allowed without --allow-non-loopback")
    backend = find_git_http_backend(args.git_http_backend)
    credentials = load_credentials(args.token_file)
    state = GatewayState(args, backend, credentials)

    handler = type("BoundGitHTTPHandler", (GitHTTPHandler,), {"state": state})
    server = http.server.ThreadingHTTPServer((args.bind, args.port), handler)
    server.daemon_threads = True
    return server


def main(argv: list[str]) -> int:
    args = parse_args(argv)
    server = make_server(args)
    repos = ", ".join(sorted(server.RequestHandlerClass.state.repos))
    sys.stderr.write(
        f"firefly-git-http listening on {args.bind}:{args.port} root={args.root} repos=[{repos}]\n"
    )
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        sys.stderr.write("shutting down\n")
    finally:
        server.server_close()
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
