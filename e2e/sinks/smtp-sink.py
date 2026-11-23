"""A minimal SMTP server for the e2e run: it takes every message and keeps
it as a file. It offers no STARTTLS and asks no password, so the panel's
mailer sends in plain text to this private address (R05-D11 allows a
private relay). Only the standard library: smtpd left Python in 3.12.

    python3 -I smtp-sink.py <folder> <port>
"""

import asyncio
import pathlib
import sys

folder = pathlib.Path(sys.argv[1])
port = int(sys.argv[2])
count = 0


async def session(reader: asyncio.StreamReader, writer: asyncio.StreamWriter) -> None:
    global count

    def say(line: str) -> None:
        writer.write((line + "\r\n").encode())

    say("220 sink ESMTP")
    await writer.drain()
    while line := await reader.readline():
        verb = line.decode(errors="replace").strip().split(" ", 1)[0].upper()
        if verb == "EHLO":
            say("250-sink")
            say("250 SIZE 10485760")
        elif verb in ("HELO", "MAIL", "RCPT", "RSET", "NOOP"):
            say("250 OK")
        elif verb == "DATA":
            say("354 end with .")
            await writer.drain()
            body = []
            while (data := await reader.readline()) not in (b".\r\n", b".\n", b""):
                body.append(data)
            count += 1
            (folder / f"{count:04d}.eml").write_bytes(b"".join(body))
            say("250 kept")
        elif verb == "QUIT":
            say("221 bye")
            await writer.drain()
            break
        else:
            say("502 not here")
        await writer.drain()
    writer.close()


async def main() -> None:
    server = await asyncio.start_server(session, "0.0.0.0", port)
    async with server:
        await server.serve_forever()


asyncio.run(main())
