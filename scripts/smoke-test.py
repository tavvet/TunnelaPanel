#!/usr/bin/env python3
"""Server-side smoke check. Credentials remain in memory and never enter argv/logs."""
import argparse
import html
import http.cookiejar
import json
from pathlib import Path
import re
import secrets
import socket
import ssl
import subprocess
import tomllib
import urllib.parse
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--panel-port", type=int, required=True)
    parser.add_argument("--panel-url", help="HTTPS panel origin instead of loopback HTTP, e.g. https://vpn.example.com:8443")
    parser.add_argument("--endpoint-port", type=int, required=True)
    parser.add_argument("--root", type=Path, default=Path("/opt/tunnelapanel"))
    parser.add_argument("--native", type=Path, default=Path("/opt/trusttunnel"))
    parser.add_argument("--exercise-users", action="store_true", help="Create/apply/disable/delete a temporary user on staging only")
    args = parser.parse_args()
    if args.exercise_users and (args.endpoint_port == 443 or args.panel_port == 8080 or args.panel_url):
        raise SystemExit("User lifecycle checks must use staging ports.")
    if args.panel_url:
        parsed = urllib.parse.urlsplit(args.panel_url)
        if parsed.scheme != "https" or not parsed.hostname or parsed.username or parsed.password or parsed.query or parsed.fragment or parsed.path not in ("", "/"):
            raise SystemExit("--panel-url must be an HTTPS origin without credentials, path, query or fragment.")
    root = args.root
    original = tomllib.loads((args.native / "credentials.toml").read_text())
    user = original["client"][0]
    hosts = tomllib.loads((root / "runtime/config/hosts.toml").read_text())
    hostname = hosts["main_hosts"][0]["hostname"]
    baseline = subprocess.run(
        [str(args.native / "trusttunnel_endpoint"), "vpn.toml", "hosts.toml", "-c", user["username"],
         "-a", hostname + ":443", "--format", "toml"], cwd=args.native, capture_output=True, text=True, check=True,
    ).stdout
    baseline = tomllib.loads(baseline)
    cookies = http.cookiejar.CookieJar()
    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(cookies))
    base = args.panel_url.rstrip("/") if args.panel_url else "http://127.0.0.1:" + str(args.panel_port)

    def get(path):
        with opener.open(base + path, timeout=75) as response:
            assert response.headers.get("Cache-Control") == "no-store", "Missing private response protection"
            return response.read()

    def csrf(page):
        match = re.search(r'name="csrf" value="([^"]+)"', page)
        assert match, "Missing CSRF token"
        return html.unescape(match[1])

    def post(path, fields):
        page = get("/users").decode()
        fields = dict(fields, csrf=csrf(page))
        request = urllib.request.Request(base + path, urllib.parse.urlencode(fields).encode(), headers={"Origin": base})
        with opener.open(request, timeout=75) as response:
            page = response.read().decode()
            assert 'class="notice notice-error"' not in page, "Panel returned an action error: " + path
            return page

    with opener.open(base + "/login", timeout=75) as response:
        assert response.headers.get("Referrer-Policy") == "same-origin", "Form POST origin is suppressed"
        login = response.read().decode()
    assert '<meta name="referrer" content="same-origin">' in login, "HTML overrides form origin policy"
    initial_password = (root / "runtime/panel/bootstrap-password").read_text()
    fields = {"username": "admin", "password": initial_password, "csrf": csrf(login)}
    request = urllib.request.Request(base + "/login", urllib.parse.urlencode(fields).encode(), headers={"Origin": base})
    with opener.open(request, timeout=75) as response:
        assert response.url.rstrip("/") == base, "Login did not redirect to dashboard"
    if args.panel_url:
        assert cookies and all(cookie.secure for cookie in cookies), "HTTPS login cookies must be Secure"
        print("OK verified HTTPS certificate and Secure cookies")
    print("OK admin login and CSRF session")

    def export(username):
        page = get("/users/export?" + urllib.parse.urlencode({"username": username})).decode()
        match = re.search(r'<textarea[^>]*id="client-toml"[^>]*>(.*?)</textarea>', page, re.S)
        assert match, "Missing client export"
        return tomllib.loads(html.unescape(match[1]))

    assert export(user["username"]) == baseline, "Imported client export differs from native configuration"
    for path in ("/", "/users", "/logs", "/settings"):
        page = get(path).decode()
        assert 'class="notice notice-error"' not in page, "Panel data unavailable: " + path
    png = get("/users/qr?" + urllib.parse.urlencode({"username": user["username"]}))
    assert png.startswith(b"\x89PNG"), "Invalid QR image"
    print("OK users, metrics, logs, certificates, QR and unchanged existing client export")

    def proxy(account, expected=True):
        # Curl reads proxy credentials from stdin, never its command-line arguments.
        config = '\n'.join([
            'proxy = ' + json.dumps("https://" + hostname + ":" + str(args.endpoint_port)),
            'proxy-user = ' + json.dumps(account["username"] + ":" + account["password"]),
            'resolve = ' + json.dumps(hostname + ":" + str(args.endpoint_port) + ":127.0.0.1"),
        ]) + '\n'
        result = subprocess.run(["curl", "--silent", "--show-error", "--max-time", "20", "--config", "-",
                                 "--output", "/dev/null", "--write-out", "%{http_code}", "https://example.com/"],
                                input=config, text=True, capture_output=True)
        success = result.returncode == 0 and result.stdout == "200"
        assert success == expected, "Unexpected proxy authentication/traffic result"

    proxy(user)
    tls = ssl.create_default_context()
    tls.set_alpn_protocols(["h2"])
    with socket.create_connection(("127.0.0.1", args.endpoint_port), timeout=10) as sock:
        with tls.wrap_socket(sock, server_hostname=hostname) as secure:
            assert secure.selected_alpn_protocol() == "h2", "HTTP/2 TLS negotiation failed"
    print("OK authenticated HTTPS forwarding and HTTP/2 TLS negotiation")

    if args.exercise_users:
        temporary = {"username": "panel_smoke_" + secrets.token_hex(4), "password": secrets.token_urlsafe(24)}
        post("/users/add", temporary)
        post("/apply", {})
        generated = export(temporary["username"])
        assert generated.get("username") == temporary["username"] and generated.get("password") == temporary["password"], "New credentials changed during TOML serialization"
        proxy(temporary)
        post("/users/edit", {"username": temporary["username"], "enabled": "false"})
        post("/apply", {})
        proxy(temporary, expected=False)
        proxy(user)
        post("/users/delete", {"username": temporary["username"]})
        post("/apply", {})
        assert tomllib.loads((root / "runtime/config/credentials.toml").read_text()) == original, "Original users not restored"
        assert export(user["username"]) == baseline, "Existing export changed after apply"
        print("OK create → apply → export → connect → disable → reject → delete; original user preserved")
    print("SMOKE CHECK PASSED")


if __name__ == "__main__":
    main()
