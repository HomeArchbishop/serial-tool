#!/usr/bin/env python3
"""Read the serial-tool depth TCP stream.

Examples:
  python3 depth_view.py                      # headless stats from 127.0.0.1:47200
  python3 depth_view.py --gui                # live window (needs numpy + opencv-python)
  python3 depth_view.py --save frame.pgm     # save one 16-bit frame
"""
import argparse
import json
import socket
import struct
import sys
import time

PACKET_HEADER = struct.Struct("<4sBBHI")
MAGIC = b"OBDP"
TYPE_META = 1
TYPE_DEPTH = 2
FRAME_HEADER = struct.Struct("<IIIIQfII")  # 36 bytes


def readn(sock, n):
    buf = bytearray()
    while len(buf) < n:
        chunk = sock.recv(n - len(buf))
        if not chunk:
            raise EOFError
        buf += chunk
    return bytes(buf)


def read_packet(sock):
    magic, version, kind, _flags, size = PACKET_HEADER.unpack(readn(sock, PACKET_HEADER.size))
    if magic != MAGIC:
        raise RuntimeError(f"bad magic: {magic!r}")
    if version != 1:
        raise RuntimeError(f"unsupported protocol version: {version}")
    return kind, readn(sock, size)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--host", default="127.0.0.1")
    ap.add_argument("--port", type=int, default=47200)
    ap.add_argument("--frames", type=int, default=0, help="stop after N depth frames (0 = forever)")
    ap.add_argument("--gui", action="store_true", help="show an OpenCV window")
    ap.add_argument("--save", help="save the first depth frame as a 16-bit PGM")
    args = ap.parse_args()

    cv2 = np = None
    if args.gui:
        try:
            import cv2
            import numpy as np
        except ImportError as exc:
            sys.exit("--gui needs numpy and opencv-python: pip install numpy opencv-python")

    sock = socket.create_connection((args.host, args.port), timeout=5)
    sock.settimeout(10)
    meta = None
    saved = False
    count = 0
    window_start = time.time()
    window_count = 0
    fps = 0.0

    try:
        while True:
            kind, payload = read_packet(sock)
            if kind == TYPE_META:
                meta = json.loads(payload)
                print(
                    "meta:", meta["width"], "x", meta["height"],
                    meta.get("formatName"), "scale", meta.get("valueScale"),
                    "device", meta.get("name"),
                )
                continue
            if kind != TYPE_DEPTH:
                continue
            if meta is None:
                print("warning: frame before meta", file=sys.stderr)

            frame_id, width, height, fmt, ts_us, scale, data_size, _ = FRAME_HEADER.unpack_from(payload)
            data = payload[FRAME_HEADER.size:]
            if len(data) < data_size:
                raise RuntimeError(f"short frame: {len(data)} < {data_size}")

            count += 1
            window_count += 1
            now = time.time()
            if now - window_start >= 0.5:
                fps = window_count / (now - window_start)
                window_start = now
                window_count = 0

            if args.save and not saved:
                with open(args.save, "wb") as f:
                    f.write(f"P5\n{width} {height}\n65535\n".encode())
                    f.write(data[:data_size])
                saved = True
                print("saved", args.save)

            if args.gui:
                arr = np.frombuffer(data[:data_size], dtype=np.uint16).reshape(height, width).astype(np.float32)
                valid = arr[arr > 0]
                if valid.size:
                    lo, hi = np.percentile(valid, [5, 95])
                    if hi <= lo:
                        hi = lo + 1
                    norm = np.clip((arr - lo) / (hi - lo), 0, 1)
                else:
                    norm = np.zeros_like(arr)
                img = cv2.applyColorMap((norm * 255).astype(np.uint8), cv2.COLORMAP_JET)
                cv2.putText(img, f"{fps:.1f} fps  id={frame_id}", (8, height - 10),
                            cv2.FONT_HERSHEY_SIMPLEX, 0.5, (255, 255, 255), 1, cv2.LINE_AA)
                cv2.imshow("serial-tool depth", img)
                if cv2.waitKey(1) & 0xFF in (27, ord("q")):
                    break
            elif count == 1 or count % 15 == 0:
                mid = (height // 2) * width + width // 2
                center = struct.unpack_from("<H", data, mid * 2)[0] * scale
                print(f"frame {frame_id} {width}x{height} fmt={fmt} size={data_size} "
                      f"center={center:.1f}mm ~{fps:.1f}fps")

            if args.frames and count >= args.frames:
                break
    except (EOFError, KeyboardInterrupt):
        pass
    finally:
        sock.close()
        if args.gui:
            cv2.destroyAllWindows()


if __name__ == "__main__":
    main()
