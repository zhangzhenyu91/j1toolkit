# -*- coding: utf-8 -*-
# 生成小程序分享图：统一背景 + 居中白色卡片 + 应用图标 / 工具箱图标
# 用法：python design/make_share_images.py
from pathlib import Path

from PIL import Image, ImageDraw, ImageFilter, ImageFont

ROOT = Path(__file__).resolve().parent.parent
BG_PATH = ROOT / 'design' / 'share-bg-preview.png'
FONT_PATH = ROOT / 'design' / 'font' / 't.ttf'
TOOLBOX_PATH = ROOT / 'miniprogram' / 'assets' / 'toolbox.png'
OUT_DIR = ROOT / 'miniprogram' / 'images' / 'share'

ORANGE = '#F26D21'  # 品牌主色
CARD_RADIUS = 48
CARD_SIZE = 460
ICON_SIZE = 300

# 应用图标：TDesign 图标名 -> 码点（与 sys_app.icon / t-icon 一致）
APPS = {
    'call-me': 0xE6EC,    # robot
    'work-log': 0xE11B,   # calendar
    'safe-day': 0xE367,   # file-safety
}

WATERMARK_CROP = 64  # 裁掉底部 AI 生成水印
SHARE_RATIO = 5 / 4  # 微信分享缩略图按 5:4 显示，源头直接用 5:4 避免被裁偏
FINAL_WIDTH = 1000   # 出图宽度（高按 5:4 = 800）：兼顾清晰度与单张 <100KB（体验评分要求图片资源不超过 200K）


def load_background() -> Image.Image:
    bg = Image.open(BG_PATH).convert('RGB')
    bg = bg.crop((0, 0, bg.width, bg.height - WATERMARK_CROP))
    # 居中裁成 5:4（微信分享图推荐比例）
    target_w = round(bg.height * SHARE_RATIO)
    x0 = (bg.width - target_w) // 2
    return bg.crop((x0, 0, x0 + target_w, bg.height))


def make_card(size: int, radius: int) -> Image.Image:
    """白色圆角卡片（带柔和投影），返回 RGBA"""
    pad = 40
    canvas = Image.new('RGBA', (size + pad * 2, size + pad * 2), (0, 0, 0, 0))
    # 投影
    shadow = Image.new('RGBA', canvas.size, (0, 0, 0, 0))
    d = ImageDraw.Draw(shadow)
    d.rounded_rectangle(
        (pad, pad + 6, pad + size, pad + size + 6), radius=radius,
        fill=(34, 49, 78, 38),
    )
    shadow = shadow.filter(ImageFilter.GaussianBlur(14))
    canvas.alpha_composite(shadow)
    # 卡体
    d = ImageDraw.Draw(canvas)
    d.rounded_rectangle((pad, pad, pad + size, pad + size), radius=radius, fill=(255, 255, 255, 255))
    return canvas


def render_glyph(codepoint: int, px: int, color: str) -> Image.Image:
    """用 TDesign 图标字体渲染单个图标为透明 PNG"""
    font = ImageFont.truetype(str(FONT_PATH), px)
    ch = chr(codepoint)
    bbox = font.getbbox(ch)
    w, h = bbox[2] - bbox[0], bbox[3] - bbox[1]
    img = Image.new('RGBA', (w, h), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)
    d.text((-bbox[0], -bbox[1]), ch, font=font, fill=color)
    return img


def compose(bg: Image.Image, icon: Image.Image) -> Image.Image:
    out = bg.copy().convert('RGBA')
    card = make_card(CARD_SIZE, CARD_RADIUS)
    cx = (out.width - card.width) // 2
    cy = (out.height - card.height) // 2
    out.alpha_composite(card, (cx, cy))
    # 图标统一缩放到 ICON_SIZE 盒内（小图放大、大图缩小）并居中于卡片
    scale = min(ICON_SIZE / icon.width, ICON_SIZE / icon.height)
    size = (round(icon.width * scale), round(icon.height * scale))
    icon = icon.resize(size, Image.LANCZOS)
    ix = (out.width - icon.width) // 2
    iy = (out.height - icon.height) // 2
    out.alpha_composite(icon, (ix, iy))
    # 出图缩到 FINAL_WIDTH 宽（5:4），控制单张体积
    final = (FINAL_WIDTH, round(FINAL_WIDTH / SHARE_RATIO))
    return out.convert('RGB').resize(final, Image.LANCZOS)


def main():
    OUT_DIR.mkdir(parents=True, exist_ok=True)
    bg = load_background()

    # 应用分享图（TDesign 图标字形，与小程序内图标完全一致）
    for key, cp in APPS.items():
        glyph = render_glyph(cp, ICON_SIZE * 2, ORANGE)
        img = compose(bg, glyph)
        path = OUT_DIR / f'share-{key}.jpg'
        img.save(path, 'JPEG', quality=80)
        print(f'{path.name}: {img.size}, {path.stat().st_size // 1024}KB')

    # 通用分享图（工具箱图标，复用 assets/toolbox.png）
    toolbox = Image.open(TOOLBOX_PATH).convert('RGBA')
    img = compose(bg, toolbox)
    path = OUT_DIR / 'share-toolbox.jpg'
    img.save(path, 'JPEG', quality=80)
    print(f'{path.name}: {img.size}, {path.stat().st_size // 1024}KB')


if __name__ == '__main__':
    main()
