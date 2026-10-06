# Draws hyperhand.ico, the gripper icon of hyperhand.exe, its tray icon and settings window (Pillow), and preview.png.
# Sizes 256, 48 and 32 are drawn large and scaled down; 24 and 16 are placed pixel by pixel.
# Regenerate the resources afterwards, in cmd/hyperhand: go-winres make --in winres/winres.json --arch amd64

from PIL import Image, ImageDraw
W = (255, 255, 255, 255)
B = (0x1E, 0x64, 0xC8, 255)

def background(S, margin, radius):
    top, bot = (0x33, 0x80, 0xE6), (0x16, 0x52, 0xB0)
    col = Image.new("RGBA", (1, S))
    for y in range(S):
        t = y / max(S - 1, 1)
        col.putpixel((0, y), tuple(int(a + (b - a) * t) for a, b in zip(top, bot)) + (255,))
    bg = col.resize((S, S))
    mask = Image.new("L", (S, S), 0)
    ImageDraw.Draw(mask).rounded_rectangle((margin, margin, S - 1 - margin, S - 1 - margin), radius=radius, fill=255)
    img = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    img.paste(bg, (0, 0), mask)
    return img

def big(bolt=True):
    S = 1024
    img = background(S, 24, 210)
    layer = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    d = ImageDraw.Draw(layer)
    def jaw(sign):  # sign -1 = left, +1 = right; mirrored about x = 512
        pts = [(-212, 640), (-212, 300), (-150, 218), (-60, 218), (-60, 290), (-112, 350), (-112, 640)]
        return [(512 + sign * -x if sign > 0 else 512 + x, y) for x, y in pts]
    d.polygon(jaw(-1), fill=W)
    d.polygon(jaw(1), fill=W)
    d.rounded_rectangle((276, 590, 748, 712), radius=40, fill=W)     # body
    d.rounded_rectangle((442, 700, 582, 880), radius=28, fill=W)     # arm
    if bolt:
        d.ellipse((470, 609, 554, 693), fill=B)
        d.ellipse((496, 635, 528, 667), fill=W)
    layer = layer.rotate(-90)
    img.alpha_composite(layer)
    return img

def pixel(n):
    art = {16: ['................', '................', '...###....###...', '...###....###...', '...##......##...', '...##......##...', '...##......##...', '...##......##...', '...##......##...', '..############..', '..############..', '......####......', '......####......', '......####......', '................', '................'],
     24: ['........................', '........................', '........................', '.....####......####.....', '.....####......####.....', '.....###........###.....', '.....###........###.....', '.....###........###.....', '.....###........###.....', '.....###........###.....', '.....###........###.....', '.....###........###.....', '.....###........###.....', '....################....', '....################....', '....################....', '..........####..........', '..........####..........', '..........####..........', '..........####..........', '..........####..........', '........................', '........................', '........................']}[n]
    img = background(n, 0, 3 if n == 16 else 4)
    for y, row in enumerate(art):
        for x, c in enumerate(row):
            if c == "#":
                img.putpixel((n - 1 - y, x), W)  # turned clockwise
    return img

order = [256, 48, 32, 24, 16]
imgs = {256: big().resize((256, 256), Image.LANCZOS), 48: big().resize((48, 48), Image.LANCZOS),
        32: big(False).resize((32, 32), Image.LANCZOS), 24: pixel(24), 16: pixel(16)}
imgs[256].save("hyperhand.ico", sizes=[(s, s) for s in order], append_images=[imgs[s] for s in order[1:]])
sheet = Image.new("RGBA", (1180, 620), (255, 255, 255, 255))
ImageDraw.Draw(sheet).rectangle((0, 310, 1180, 620), fill=(32, 32, 32, 255))
for yo in (10, 320):
    x = 10
    for s in order:
        z = imgs[s].resize((200, 200), Image.NEAREST if s < 256 else Image.LANCZOS)
        sheet.alpha_composite(z, (x, yo)); x += 210
    x = 10
    for s in order[1:]:
        sheet.alpha_composite(imgs[s], (x, yo + 230)); x += 70
sheet.save("preview.png")
print("ok")
