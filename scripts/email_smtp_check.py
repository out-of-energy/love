#!/usr/bin/env python3
"""Verify that the daily-email push channel can actually reach the user.

This is the one external dependency in the whole design, and the easiest to get
wrong (authorization code, SMTP not enabled, rate limits, spam filtering). It is
worth proving before any of the memory engine is written.

Usage:
    # connection-only check, no credentials needed
    python3 scripts/email_smtp_check.py --dry-run

    # real send
    export QQ_SMTP_AUTH_CODE="<16-digit code>"
    python3 scripts/email_smtp_check.py 848525382@qq.com

The authorization code is read from the environment only. It is never printed,
never written to disk, and never included in any error message this script
produces.

QQ Mail does NOT accept the account password over SMTP. You must generate a
16-digit authorization code: QQ Mail web -> 设置 -> 账户 ->
"POP3/IMAP/SMTP/Exchange/CardDAV/CalDAV服务" -> enable "IMAP/SMTP服务" ->
生成授权码 (requires SMS verification).
"""

import argparse
import os
import smtplib
import ssl
import sys
import time
from email.message import EmailMessage
from email.utils import formatdate, make_msgid

SMTP_HOST = os.environ.get("SMTP_HOST", "smtp.qq.com")
SMTP_PORT = int(os.environ.get("SMTP_PORT", "465"))
AUTH_CODE = os.environ.get("QQ_SMTP_AUTH_CODE", "")


def redact(text):
    """Never let the authorization code reach the terminal."""
    if AUTH_CODE and AUTH_CODE in text:
        return text.replace(AUTH_CODE, "<redacted>")
    return text


def connect():
    print(f"connecting to {SMTP_HOST}:{SMTP_PORT} (implicit TLS)...")
    started = time.monotonic()
    ctx = ssl.create_default_context()
    server = smtplib.SMTP_SSL(SMTP_HOST, SMTP_PORT, timeout=20, context=ctx)
    server.ehlo()
    print(f"  connected in {time.monotonic() - started:.2f}s")

    code, banner = 250, ""
    try:
        code, banner = server.ehlo()
    except smtplib.SMTPException as exc:
        print(f"  EHLO failed: {redact(str(exc))}")
        raise

    caps = {}
    try:
        caps = server.esmtp_features or {}
    except Exception:
        pass
    print(f"  EHLO ok: {banner.decode(errors='replace') if isinstance(banner, bytes) else banner}")
    for name in ("auth", "starttls", "size"):
        if name in caps:
            value = str(caps[name]).strip()
            if name == "auth":
                print(f"  advertised AUTH mechanisms: {value}")
            else:
                print(f"  {name}: {value}")
    return server


def send(server, address, subject, body):
    message = EmailMessage()
    message["From"] = address
    message["To"] = address
    message["Subject"] = subject
    message["Date"] = formatdate(localtime=True)
    message["Message-ID"] = make_msgid(domain="love.local")
    message["Auto-Submitted"] = "auto-generated"
    message.set_content(body, charset="utf-8")

    print(f"authenticating as {address}...")
    try:
        server.login(address, AUTH_CODE)
    except smtplib.SMTPAuthenticationError as exc:
        print(f"  AUTH FAILED ({exc.smtp_code}): {redact(str(exc))}")
        print()
        print("  Most likely causes:")
        print("    1. QQ_SMTP_AUTH_CODE is the account password, not the 16-digit code")
        print("    2. IMAP/SMTP service is not enabled in QQ Mail settings")
        print("    3. The code was regenerated and this one is now stale")
        return False
    print("  authenticated")

    started = time.monotonic()
    refused = server.send_message(message)
    elapsed = time.monotonic() - started
    if refused:
        print(f"  server refused recipients: {refused}")
        return False
    print(f"  accepted for delivery in {elapsed:.2f}s")
    return True


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("address", nargs="?", default="848525382@qq.com", help="recipient (and sender) address")
    parser.add_argument("--dry-run", action="store_true", help="connect and EHLO only, never send")
    args = parser.parse_args()

    print("=== SMTP channel check ===")
    if not AUTH_CODE:
        print("QQ_SMTP_AUTH_CODE is not set; running the connection check only.")
        args.dry_run = True

    try:
        server = connect()
    except Exception as exc:
        print(f"  CONNECTION FAILED: {redact(str(exc))}")
        print("\nRESULT: cannot reach the SMTP server.")
        return 1

    try:
        if args.dry_run:
            print("\nRESULT: server reachable, EHLO ok. (no message sent)")
            return 0

        subject = "love · 每日英语复习"
        body = (
            "这是 love 的通道测试邮件。\n\n"
            "如果你在手机上看到了它，说明每日推送通道可用：\n"
            "  单词与例句会通过邮件送达，方便随时瞄一眼；\n"
            "  评分仍然回到电脑上批量完成。\n\n"
            "maintain /meɪnˈteɪn/\n"
            "ELI5: To keep something working well.\n"
            "中文：维护，保持\n"
        )
        ok = send(server, args.address, subject, body)
    finally:
        try:
            server.quit()
        except Exception:
            pass

    print()
    if ok:
        print(f"RESULT: sent to {args.address}.")
        print("Check the inbox (and the spam folder) on the phone.")
        return 0
    print("RESULT: send failed.")
    return 1


if __name__ == "__main__":
    sys.exit(main())
