#!/usr/bin/env python3
"""用真实 pty + pyte 还原离线开屏，保存屏幕、ANSI trace 和 PNG 供审核。"""

import argparse
import codecs
import fcntl
import json
import os
from pathlib import Path
import pty
import select
import struct
import subprocess
import tempfile
import termios
import time

import pyte
from PIL import Image, ImageDraw, ImageFont


def capture(cols, rows, color_term):
    screen = pyte.Screen(cols, rows)
    stream = pyte.Stream(screen)
    decoder = codecs.getincrementaldecoder("utf-8")("replace")
    trace = bytearray()
    master, slave = pty.openpty()
    fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))
    with tempfile.TemporaryDirectory(prefix="plume-preview-") as plume_home:
        env = dict(os.environ, PLUME_HOME=plume_home, TERM="xterm-256color")
        env["COLORTERM"] = color_term
        # 预览显式检查颜色能力，不继承自动化宿主的无色/强制颜色选项。
        for key in ("NO_COLOR", "CLICOLOR", "CLICOLOR_FORCE", "FORCE_COLOR"):
            env.pop(key, None)
        proc = subprocess.Popen(
            ["./plume", "chat", "--offline"],
            stdin=slave, stdout=slave, stderr=slave, env=env,
        )
        os.close(slave)

        def drain(seconds):
            deadline = time.monotonic() + seconds
            while time.monotonic() < deadline:
                ready, _, _ = select.select([master], [], [], 0.1)
                if not ready:
                    continue
                try:
                    data = os.read(master, 65536)
                except OSError:
                    break
                trace.extend(data)
                stream.feed(decoder.decode(data))
                # 应答背景/光标/终端能力查询，避免初始化等待超时。
                for query, response in (
                    (b"\x1b]11;?", b"\x1b]11;rgb:0000/0000/0000\x1b\\"),
                    (b"\x1b[6n", b"\x1b[1;1R"),
                    (b"\x1b[c", b"\x1b[?1;2c"),
                    (b"\x1b[?u", b"\x1b[?0u"),
                ):
                    if query in data:
                        os.write(master, response)

        try:
            drain(4)
            display = "\n".join(line.rstrip() for line in screen.display)
            startup_trace = bytes(trace)
            image = render(screen)
            # 空输入下 Ctrl+D 退出，退出前的屏幕帧独立保存。
            os.write(master, b"\x04")
            drain(1)
            proc.wait(timeout=5)
        finally:
            if proc.poll() is None:
                proc.kill()
                proc.wait()
            os.close(master)

    checks = {
        "version_visible": "plume-agent v" in display.lower(),
        "offline_visible": "fake/offline" in display,
        "clean_exit": proc.returncode == 0,
    }
    if cols >= 80:
        frame = display.splitlines()
        top = next((line for line in frame if line.startswith("╭")), "")
        bottom = next((line for line in frame if line.startswith("╰")), "")
        sides = [line for line in frame if line.startswith("│")]
        checks.update({
            "tips_visible": "Tips for getting started" in display,
            "braille_visible": any("\u2800" < char <= "\u28ff" for char in display),
            "closed_box": top.endswith("╮") and bottom.endswith("╯"),
            "aligned_box": len(top) == len(bottom) == min(cols - 2, 88)
            and len(sides) == 20
            and all(len(line) == len(top) and line.endswith("│") for line in sides),
        })
        checks["theme_colors"] = (
            all(rgb in startup_trace for rgb in (
                b"38;2;58;158;163", b"38;2;91;200;200", b"38;2;125;211;216",
            )) if color_term == "truecolor" else b"38;5;" in startup_trace
        )
    else:
        checks["plain_layout"] = "╭" not in display and "Tips for getting started" not in display
        if cols >= 32:
            checks["braille_visible"] = any("\u2800" < char <= "\u28ff" for char in display)
    return image, display, startup_trace, checks


def render(screen):
    candidates = (
        "/System/Library/Fonts/SFNSMono.ttf",
        "/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf",
    )
    font_path = next((path for path in candidates if Path(path).is_file()), None)
    if font_path is None:
        raise RuntimeError("A monospace font is required to render the preview")
    font = ImageFont.truetype(font_path, 20)
    cell_w = round(font.getlength("M"))
    cell_h = cell_w * 2
    margin = 24
    image = Image.new("RGB", (screen.columns * cell_w + 2 * margin,
                              screen.lines * cell_h + 2 * margin), "black")
    draw = ImageDraw.Draw(image)
    # 终端默认黑底；颜色来自实际 ANSI 帧，不另设产品主题。
    colors = {
        "black": "000000", "red": "800000", "green": "008000",
        "brown": "808000", "blue": "000080", "magenta": "800080",
        "cyan": "008080", "white": "c0c0c0", "brightblack": "808080",
        "brightred": "ff0000", "brightgreen": "00ff00", "brightbrown": "ffff00",
        "brightblue": "0000ff", "brightmagenta": "ff00ff",
        "brightcyan": "00ffff", "brightwhite": "ffffff",
    }

    def color(value, default):
        return "#" + (default if value == "default" else colors.get(value, value))

    ascent, descent = font.getmetrics()
    baseline = (cell_h - ascent - descent) // 2 + ascent
    for row in range(screen.lines):
        for col in range(screen.columns):
            char = screen.buffer[row][col]
            x, y = margin + col * cell_w, margin + row * cell_h
            fg, bg = color(char.fg, "ffffff"), color(char.bg, "000000")
            draw.rectangle((x, y, x + cell_w - 1, y + cell_h - 1), fill=bg)
            # 盲文按捕获字符的点位还原圆点，避免 SF Mono 缺少字形导致空白。
            if len(char.data) == 1 and "\u2800" <= char.data <= "\u28ff":
                mask = ord(char.data) - 0x2800
                radius = min(cell_w / 2, cell_h / 4) * 0.18
                for dy, bits in enumerate(((0, 3), (1, 4), (2, 5), (6, 7))):
                    for dx, bit in enumerate(bits):
                        if mask & (1 << bit):
                            cx = x + cell_w * (2 * dx + 1) / 4
                            cy = y + cell_h * (2 * dy + 1) / 8
                            draw.ellipse((cx - radius, cy - radius, cx + radius, cy + radius), fill=fg)
            # 块字按单元格覆盖范围还原，避免字体留白造成拼接缝。
            elif char.data in ("█", "▀", "▄"):
                y0 = y + cell_h // 2 if char.data == "▄" else y
                y1 = y + cell_h // 2 - 1 if char.data == "▀" else y + cell_h - 1
                draw.rectangle((x, y0, x + cell_w - 1, y1), fill=fg)
            elif char.data.strip():
                draw.text((x, y + baseline), char.data, font=font, fill=fg, anchor="ls")
    return image


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--cols", type=int, default=100)
    parser.add_argument("--rows", type=int, default=32)
    parser.add_argument("--color-term", default="truecolor")
    parser.add_argument("--output", type=Path, default=Path("docs/reviews/evidence/G1b.2.2"))
    args = parser.parse_args()
    image, display, trace, checks = capture(args.cols, args.rows, args.color_term)
    args.output.mkdir(parents=True, exist_ok=True)
    name = f"splash-braille-{args.cols}x{args.rows}-{'truecolor' if args.color_term else '256color'}"
    image.save(args.output / f"{name}.png")
    (args.output / f"{name}.txt").write_text(display + "\n", encoding="utf-8")
    (args.output / f"{name}.ansi").write_bytes(trace)
    (args.output / f"{name}.json").write_text(json.dumps({
        "columns": args.cols, "rows": args.rows, "checks": checks,
    }, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({"preview": str(args.output / f"{name}.png"), "checks": checks}))
    return 0 if all(checks.values()) else 1


if __name__ == "__main__":
    raise SystemExit(main())
