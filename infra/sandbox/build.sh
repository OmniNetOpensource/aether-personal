#!/usr/bin/env bash
# 本脚本安装固定版本的桌面工具与 AIO，并把用户主目录指向未来挂载的工作区；供 Docker 与 FC 模板构建共用。
set -euo pipefail

export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends \
    curl ca-certificates ripgrep tmux python3 python3-pil nodejs npm xvfb x11-utils dbus-x11 \
    libatspi2.0-0 at-spi2-core libgtk-3-0 libxdo3 xdotool xclip xfwm4 ffmpeg util-linux \
    fonts-noto-cjk fonts-noto-cjk-extra fonts-dejavu-core fonts-liberation poppler-utils \
    x11vnc websockify
rm -rf /var/lib/apt/lists/*

# 安装无头 PDF 幻灯片渲染所需的 Playwright 驱动与本地 ECharts 静态包，复用系统已装 Chrome。
mkdir -p /opt/aether/node /opt/aether/skills/slides
PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 npm install --prefix /opt/aether/node --no-save playwright-core echarts

# 写入幻灯片制作规范、DOM 越界校验与矢量 PDF 导出脚本、缩略图拼图脚本。
cat <<'EOF' > /opt/aether/skills/slides/SKILL.md
# Slide Deck (Vector PDF) Skill

Use this workflow whenever creating a presentation, slide deck, pitch deck, or report deck delivered as PDF.

## Toolchain Paths
- Render & layout check: `node /opt/aether/skills/slides/render_slides.cjs <input.html> <output.pdf>`
- Thumbnail grid generator: `python3 /opt/aether/skills/slides/thumbnail.py <input.pdf> <preview_dir>`
- Local ECharts bundle (offline, vector SVG): `file:///opt/aether/node/node_modules/echarts/dist/echarts.min.js`

## 1. Plan Before Coding
1. Outline the narrative slide by slide so each slide communicates **one clear takeaway** with a concrete title (assertion/insight rather than vague topic labels).
2. Plan **layout diversity** across consecutive slides. Never repeat the same layout twice in a row. Alternate among:
   - Hero cover (dark or rich editorial surface, bold title, subtitle, metadata strip)
   - Asymmetric split (narrative left + big visual/chart/code right)
   - KPI / metric spotlight (2–4 high-contrast big numbers + brief context)
   - Comparison matrix / two-column contrast / structured table
   - Process timeline / architecture flow (inline SVG or clean numbered cards)
   - Data visualization slide (ECharts with `renderer: 'svg'`, `animation: false`)
   - Closing / takeaways / next steps slide
3. Keep information density disciplined: aim for 25–35% open whitespace, concise bullets (<= 15–20 words per item), and no walls of prose.

## 2. Visual System & Typography
Pick **one** cohesive palette suited to the topic (avoid generic purple-on-white gradients):
- Define CSS variables in `:root` for background, surface, border, primary text, muted text, and 1–2 accents.
- CJK & Latin font stack (preinstalled in sandbox):
  `font-family: "Noto Sans CJK SC", "Noto Sans SC", "Liberation Sans", "DejaVu Sans", sans-serif;`
  For editorial serif titles: `"Noto Serif CJK SC", "Noto Serif SC", Georgia, serif;`
  For code/metrics: `"DejaVu Sans Mono", monospace;`
- Minimum readable sizes on a `1280x720` canvas:
  - Cover title: `44–56px`
  - Slide title (`h1`/`h2`): `28–36px`
  - Card title: `18–22px`
  - Body text: `>= 16px` (preferred `17–19px`, line-height `1.45–1.55`)
  - Footnotes / badges / axis labels: `>= 12–13px`
- Use generous slide padding (`48px 56px`) and strong text-to-background contrast.

## 3. Mandatory HTML / CSS Print Rules
```css
@page {
  size: 1280px 720px;
  margin: 0;
}
* {
  box-sizing: border-box;
  -webkit-print-color-adjust: exact;
  print-color-adjust: exact;
}
html, body {
  margin: 0;
  padding: 0;
}
.slide {
  width: 1280px;
  height: 720px;
  position: relative;
  overflow: hidden;
  break-after: page;
  page-break-after: always;
}
.slide:last-child {
  break-after: auto;
  page-break-after: auto;
}
```
- Mark purely decorative background shapes (glow blobs, corner watermarks) with `data-decorative="true"` so overflow checks focus on real content.
- For ECharts, always initialize with `{ renderer: 'svg' }` and set `animation: false` so every chart prints as crisp vector graphics with no partial-animation frames.

## 4. Render, Overflow Check, and Visual QA Loop
1. Run `node /opt/aether/skills/slides/render_slides.cjs deck.html deck.pdf`.
   - This checks DOM bounding boxes (`scrollHeight > clientHeight`, clipped text, out-of-bounds elements, broken images) and verifies the exported PDF page count equals the `.slide` count.
   - If it reports any overflow or page-count mismatch, fix the HTML/CSS (shorten copy, adjust grid proportions, reduce card count) and re-run until it exits `0`.
2. Run `python3 /opt/aether/skills/slides/thumbnail.py deck.pdf ./preview`.
3. Inspect `./preview/thumbnails.png` (or `./preview/thumbnails-*.png`) using `sandbox_read_image`.
   - For dense slides, charts, or any tile that looks tight, also inspect `./preview/slide-XX.png` directly with `sandbox_read_image`.
   - Check: text truncation, awkward wrapping, low contrast, overlapping chart labels, unbalanced whitespace, or repetitive slide layouts.
4. Fix every visual flaw in `deck.html`, re-run `render_slides.cjs` and `thumbnail.py`, and re-verify with `sandbox_read_image` before final delivery.

## 5. Delivery
- Call `sandbox_send_file` **only on the final `.pdf`** (e.g. `/mnt/workspace/slides/deck.pdf`).
- Keep HTML drafts, preview grids, and single-page PNGs inside `/mnt/workspace`; do not send intermediate files or thumbnails to chat unless the user asks for them.
EOF

cat <<'EOF' > /opt/aether/skills/slides/render_slides.cjs
#!/usr/bin/env node
// Render a self-contained .slide HTML deck to vector PDF via system Google Chrome, failing on content overflow or page-count mismatch.
const fs = require('node:fs');
const path = require('node:path');
const { pathToFileURL } = require('node:url');
const { execFileSync } = require('node:child_process');
const { chromium } = require('/opt/aether/node/node_modules/playwright-core');

function parseArgs(argv) {
  const args = argv.slice(2);
  if (args.length < 2 || args.includes('--help') || args.includes('-h')) {
    console.error('Usage: render_slides.cjs <input.html> <output.pdf> [--width 1280] [--height 720] [--allow-overflow]');
    process.exit(1);
  }
  let width = 1280;
  let height = 720;
  let allowOverflow = false;
  const positional = [];
  for (let i = 0; i < args.length; i++) {
    if (args[i] === '--width') {
      width = Number(args[++i]);
    } else if (args[i] === '--height') {
      height = Number(args[++i]);
    } else if (args[i] === '--allow-overflow') {
      allowOverflow = true;
    } else {
      positional.push(args[i]);
    }
  }
  if (positional.length !== 2 || !Number.isFinite(width) || !Number.isFinite(height) || width <= 0 || height <= 0) {
    console.error('Invalid arguments.');
    process.exit(1);
  }
  return { inputPath: path.resolve(positional[0]), outputPath: path.resolve(positional[1]), width, height, allowOverflow };
}

async function main() {
  const { inputPath, outputPath, width, height, allowOverflow } = parseArgs(process.argv);
  if (!fs.existsSync(inputPath)) {
    console.error(`Input HTML not found: ${inputPath}`);
    process.exit(1);
  }
  fs.mkdirSync(path.dirname(outputPath), { recursive: true });

  const browser = await chromium.launch({
    executablePath: '/usr/bin/google-chrome',
    headless: true,
    args: ['--no-sandbox', '--disable-dev-shm-usage'],
  });

  try {
    const page = await browser.newPage({ viewport: { width, height }, deviceScaleFactor: 2 });
    const runtimeErrors = [];
    page.on('pageerror', (err) => runtimeErrors.push(`pageerror: ${err.message}`));
    page.on('console', (msg) => {
      if (msg.type() === 'error') runtimeErrors.push(`console.error: ${msg.text()}`);
    });

    await page.goto(pathToFileURL(inputPath).href, { waitUntil: 'networkidle' });
    await page.evaluate(async () => {
      if (document.fonts && document.fonts.ready) await document.fonts.ready;
      await new Promise((resolve) => setTimeout(resolve, 250));
    });

    const inspection = await page.evaluate(({ expectedWidth, expectedHeight }) => {
      const slides = Array.from(document.querySelectorAll('.slide'));
      const issues = [];
      if (slides.length === 0) {
        issues.push('No .slide elements found. Wrap each slide in <section class="slide">.');
        return { slideCount: 0, issues };
      }

      const describe = (el) => {
        const tag = el.tagName.toLowerCase();
        const cls = typeof el.className === 'string' && el.className.trim() ? '.' + el.className.trim().split(/\s+/).slice(0, 2).join('.') : '';
        const text = (el.textContent || '').replace(/\s+/g, ' ').trim().slice(0, 60);
        return text ? `${tag}${cls} ("${text}")` : `${tag}${cls}`;
      };

      const hasDirectText = (el) =>
        Array.from(el.childNodes).some((node) => node.nodeType === Node.TEXT_NODE && node.textContent.trim().length > 0);

      slides.forEach((slide, idx) => {
        const slideNum = idx + 1;
        const slideRect = slide.getBoundingClientRect();
        if (Math.abs(slideRect.width - expectedWidth) > 2 || Math.abs(slideRect.height - expectedHeight) > 2) {
          issues.push(`Slide ${slideNum}: size ${Math.round(slideRect.width)}x${Math.round(slideRect.height)} != ${expectedWidth}x${expectedHeight}`);
        }
        if (slide.scrollHeight > slide.clientHeight + 2) {
          issues.push(`Slide ${slideNum}: vertical overflow on .slide (scrollHeight=${slide.scrollHeight} > clientHeight=${slide.clientHeight})`);
        }
        if (slide.scrollWidth > slide.clientWidth + 2) {
          issues.push(`Slide ${slideNum}: horizontal overflow on .slide (scrollWidth=${slide.scrollWidth} > clientWidth=${slide.clientWidth})`);
        }

        const imgs = Array.from(slide.querySelectorAll('img'));
        for (const img of imgs) {
          if (!img.complete || img.naturalWidth === 0) {
            issues.push(`Slide ${slideNum}: broken image (${img.getAttribute('src') || 'no src'})`);
          }
        }

        const nodes = Array.from(slide.querySelectorAll('*'));
        for (const el of nodes) {
          if (el.closest('[data-decorative="true"]') || el.closest('svg')) continue;
          const style = window.getComputedStyle(el);
          if (style.display === 'none' || style.visibility === 'hidden') continue;
          const rect = el.getBoundingClientRect();
          if (rect.width === 0 && rect.height === 0) continue;

          const clips = style.overflowY === 'hidden' || style.overflowY === 'auto' || style.overflowY === 'scroll' ||
                        style.overflowX === 'hidden' || style.overflowX === 'auto' || style.overflowX === 'scroll';
          if (clips && el.clientHeight > 0 && el.scrollHeight > el.clientHeight + 3) {
            issues.push(`Slide ${slideNum}: vertical content clipped inside ${describe(el)} (scrollHeight=${el.scrollHeight} > clientHeight=${el.clientHeight})`);
          }
          if (clips && el.clientWidth > 0 && el.scrollWidth > el.clientWidth + 3) {
            issues.push(`Slide ${slideNum}: horizontal content clipped inside ${describe(el)} (scrollWidth=${el.scrollWidth} > clientWidth=${el.clientWidth})`);
          }
          if (hasDirectText(el)) {
            if (rect.bottom > slideRect.bottom + 2 || rect.top < slideRect.top - 2 || rect.right > slideRect.right + 2 || rect.left < slideRect.left - 2) {
              issues.push(`Slide ${slideNum}: text outside slide bounds at ${describe(el)}`);
            }
          }
        }
      });

      return { slideCount: slides.length, issues };
    }, { expectedWidth: width, expectedHeight: height });

    const allIssues = [...runtimeErrors, ...inspection.issues];
    if (allIssues.length > 0) {
      console.error('Layout/render issues detected:');
      for (const issue of allIssues.slice(0, 30)) {
        console.error(`  - ${issue}`);
      }
      if (allIssues.length > 30) {
        console.error(`  ... and ${allIssues.length - 30} more issues`);
      }
      if (!allowOverflow) {
        process.exit(2);
      }
    }

    await page.pdf({
      path: outputPath,
      width: `${width}px`,
      height: `${height}px`,
      printBackground: true,
      preferCSSPageSize: true,
      margin: { top: '0px', right: '0px', bottom: '0px', left: '0px' },
    });

    const infoOut = execFileSync('pdfinfo', [outputPath], { encoding: 'utf8' });
    const match = infoOut.match(/^Pages:\s+(\d+)/m);
    const pdfPages = match ? Number(match[1]) : 0;
    if (pdfPages !== inspection.slideCount) {
      console.error(`PDF page count mismatch: expected ${inspection.slideCount} slides, got ${pdfPages} PDF pages (check @page size or trailing page break).`);
      process.exit(3);
    }

    console.log(JSON.stringify({ ok: true, slides: inspection.slideCount, pdfPages, output: outputPath }));
  } finally {
    await browser.close();
  }
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
EOF

cat <<'EOF' > /opt/aether/skills/slides/thumbnail.py
#!/usr/bin/env python3
"""Render PDF pages with pdftoppm and assemble labeled contact-sheet grids for visual inspection."""
import argparse
import glob
import math
import os
import re
import subprocess
import sys
from PIL import Image, ImageDraw, ImageFont


def natural_key(path):
    return [int(part) if part.isdigit() else part for part in re.split(r'(\d+)', os.path.basename(path))]


def build_grid(tile_paths, start_number, cols, tile_width, out_path):
    first = Image.open(tile_paths[0])
    tile_height = round(tile_width * first.height / first.width)
    first.close()

    padding = 20
    gap = 16
    rows = math.ceil(len(tile_paths) / cols)
    canvas_w = padding * 2 + cols * tile_width + (cols - 1) * gap
    canvas_h = padding * 2 + rows * tile_height + (rows - 1) * gap
    canvas = Image.new('RGB', (canvas_w, canvas_h), (229, 231, 235))
    draw = ImageDraw.Draw(canvas)
    font = ImageFont.load_default()

    for idx, tile_path in enumerate(tile_paths):
        row = idx // cols
        col = idx % cols
        x = padding + col * (tile_width + gap)
        y = padding + row * (tile_height + gap)
        with Image.open(tile_path) as raw:
            resized = raw.convert('RGB').resize((tile_width, tile_height), Image.Resampling.LANCZOS)
        canvas.paste(resized, (x, y))
        draw.rectangle([x - 1, y - 1, x + tile_width, y + tile_height], outline=(148, 163, 184), width=2)

        label = f"#{start_number + idx}"
        badge_w = 18 + len(label) * 7
        draw.rectangle([x + 8, y + 8, x + 8 + badge_w, y + 30], fill=(15, 23, 42))
        draw.text((x + 15, y + 13), label, fill=(255, 255, 255), font=font)

    canvas.save(out_path, format='PNG', optimize=True)
    # Keep each grid under 3.8 MiB so sandbox_read_image never hits the 4 MiB image limit.
    if os.path.getsize(out_path) > 3_800_000:
        jpg_path = os.path.splitext(out_path)[0] + '.jpg'
        canvas.save(jpg_path, format='JPEG', quality=88, optimize=True)
        os.remove(out_path)
        return jpg_path
    return out_path


def main():
    parser = argparse.ArgumentParser(description='Render PDF slides into PNGs and thumbnail grids.')
    parser.add_argument('input_pdf')
    parser.add_argument('output_dir')
    parser.add_argument('--cols', type=int, default=3)
    parser.add_argument('--dpi', type=int, default=150)
    parser.add_argument('--max-per-grid', type=int, default=12)
    args = parser.parse_args()

    os.makedirs(args.output_dir, exist_ok=True)
    for old in glob.glob(os.path.join(args.output_dir, 'slide-*.png')) + glob.glob(os.path.join(args.output_dir, 'thumbnails*')):
        os.remove(old)

    prefix = os.path.join(args.output_dir, 'slide')
    subprocess.run(['pdftoppm', '-png', '-r', str(args.dpi), args.input_pdf, prefix], check=True)

    slides = sorted(glob.glob(os.path.join(args.output_dir, 'slide-*.png')), key=natural_key)
    if not slides:
        print('No slide PNGs produced.', file=sys.stderr)
        sys.exit(1)

    grids = []
    chunks = [slides[i:i + args.max_per_grid] for i in range(0, len(slides), args.max_per_grid)]
    for chunk_idx, chunk in enumerate(chunks, start=1):
        name = 'thumbnails.png' if len(chunks) == 1 else f'thumbnails-{chunk_idx}.png'
        out_path = os.path.join(args.output_dir, name)
        saved = build_grid(chunk, (chunk_idx - 1) * args.max_per_grid + 1, args.cols, 600, out_path)
        grids.append(saved)

    for grid in grids:
        print(f"GRID {grid} ({os.path.getsize(grid)} bytes)")
    for slide in slides:
        print(f"SLIDE {slide} ({os.path.getsize(slide)} bytes)")


if __name__ == '__main__':
    main()
EOF

chmod -R a+rX /opt/aether/node /opt/aether/skills
chmod 755 /opt/aether/skills/slides/render_slides.cjs /opt/aether/skills/slides/thumbnail.py

# 固定下载地址与摘要，使快照和自有镜像使用相同的守护进程。
curl --fail --show-error --location https://aio-static.tos-cn-beijing.volces.com/v0.9.2/linux-x86_64/aiod -o /usr/local/bin/aiod
curl --fail --show-error --location https://aio-static.tos-cn-beijing.volces.com/v0.9.2/linux-x86_64/computer-use -o /usr/local/bin/computer-use
echo '1f52cb04e3e6e9df42e0937db060b390c3220af49a14e17bee8696ab9f11faae  /usr/local/bin/aiod' | sha256sum -c -
echo '7b8458e9e89ec6206eea36505efe55b8630e711adcff75afeca033029439405e  /usr/local/bin/computer-use' | sha256sum -c -
chmod 755 /usr/local/bin/aiod /usr/local/bin/computer-use /opt/aether/start.sh

# 主目录和显式浏览器配置都落在挂载点；保留基础镜像的旧主目录供运维查看。
test "$(id -u user)" = 1000
test "$(id -g user)" = 1000
command -v google-chrome
install -d -o 1000 -g 1000 /mnt/workspace
mv /home/user /opt/aether-default-home
ln -s /mnt/workspace /home/user
usermod -d /mnt/workspace user
