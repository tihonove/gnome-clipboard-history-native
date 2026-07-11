# Image support in the clipboard (gnome-clipboard-history-native)

## Why this is needed

Right now gnome-clipboard-history-native is text-only and in-memory. The next step is to capture copying
of images (screenshots from GNOME Screenshot/Spectacle/Flameshot, "Copy image" from
browsers, copying files from Nautilus), show a thumbnail in the popup list, and paste
the image back into the target application via XTEST + selection ownership.

Images are where CopyQ accumulated the most pain: hangs on large images, destruction of
transparency, conversion to BMP on re-paste, duplicate thumbnails, "images from the
browser saved as text". This document is a digest of what CopyQ stepped on, so that we
don't repeat its mistakes before we even start writing code.

Source: issues from the `hluk/CopyQ` repository (the `#NNNN` numbers are cited
verbatim).

---

## MIME/targets on X11

### What actually sits in the clipboard when an image is copied

The full TARGETS list CopyQ saw when copying an image on X11
(from #973, output of `copyq clipboard '?'`):

```
image/png
image/bmp   image/x-bmp   image/x-MS-bmp
image/jpeg
image/tiff
image/x-icon  image/x-ico  image/x-win-bitmap  image/vnd.microsoft.icon
application/x-qt-image   (this is Qt-specific, we don't need it)
text/html
text/uri-list
text/plain / COMPOUND_TEXT / UTF8_STRING
```

Key takeaway: the source application usually **offers several formats
simultaneously**. The image source, as selection owner, converts the data lazily, on
request for a specific target. We (as the recipient) choose which target to request and
store.

### Format priority for storage/render

CopyQ renders the preview by iterating over formats in a fixed order. Initially this was
`SVG, BMP, PNG, JPEG, GIF`, and because BMP was above PNG, **transparency was lost**
(#40). After the fix, PNG was raised above BMP.

Recommended preference order for gnome-clipboard-history-native (what to request/store when several are
available):

1. `image/png` — priority #1. Lossless, supports alpha, all applications and GTK
   read/write it natively. This is our primary format.
2. `image/svg+xml` — store only if PNG is absent. SVG can be either higher quality
   (vector) or lower (#2961: someone's SVG from Word was worse than PNG). Rendering SVG
   is harder; CopyQ prefers PNG by default precisely for the simplicity of rendering.
3. `image/jpeg` — if the source gave only JPEG, store the **original JPEG bytes** (see
   #2185 below — don't silently re-encode to PNG/BMP).
4. `image/tiff`, `image/bmp`, `image/gif` — store the original bytes as a fallback if
   there's nothing better.

Icon formats (`image/x-icon`, `image/vnd.microsoft.icon`, etc.) we ignore — that's
noise from the source application.

### Recommended storage strategy

We store the **original bytes of exactly the target the source gave**, plus the name of
the target itself. We don't aggressively normalize into a single internal format. The
reason is the fidelity section below (#2185, #2961, #2961-SVG). Separately we keep a
decoded/downscaled QPixmap-analog (in our case a `GdkPixbuf`) **only for the
thumbnail**, not for handing to the selection.

Minimal image record:

```
{
  targets: {                     // what we actually stored from the source
    "image/png":  <bytes>,       // primary; may be the only one
    "image/svg+xml": <bytes>,    // optional
    ...
  },
  primary_target: "image/png",   // what we render and what we hand over by default
  thumb: <GdkPixbuf, already downscaled>,
  hash:  <sha256 of the primary bytes>,
  w, h, byte_size
}
```

### About text/uri-list and x-special/gnome-copied-files

When an image is copied **in a file manager** (Nautilus) or from a browser via "Copy
image" in chromium-like browsers, there are **no image data** in the clipboard — there's
only:

- `text/uri-list` + `x-special/gnome-copied-files` (Nautilus: `copy\nfile:///...`)
- or `text/html` with `<img src="http...">` and `text/plain` with the link.

CopyQ got burned on this many times (#1100, #2046, #2084, #2936, #3369, #2858, #1591):
users expect a preview, but only a path/URL sits in the clipboard, and they get an empty
element or a line of text. See the fidelity section — this is the most frequent
complaint.

Solution for gnome-clipboard-history-native: a uri-list to a local image file we can **read from disk
ourselves** and render a thumbnail (it's a local file, not the network — without
security/perf problems, unlike `<img src=http>`). A remote URL from HTML we **do not
download** (CopyQ refused on principle — #1591, #2084 — due to security and performance;
we agree).

---

## CopyQ's problems and how to avoid them

### 1. Memory and performance on large images

- **#1070 "Can't pipe large images into xclip while CopyQ is running"** (23
  comments). While CopyQ (as a clipboard manager) is running and reaching into the
  clipboard, `xclip -target image/png < big.png` crashes with `BadWindow
  (X_ChangeProperty)`. It reproduced even with the Images plugin **disabled** and even
  with KolourPaint — i.e. the root cause is that any listener that pulls at the selection
  during the INCR transfer of a large image breaks the transfer to the source
  application. It crashed GIMP.
  → gnome-clipboard-history-native recommendation: when the selection owner is handing over large data,
  **don't request the data synchronously in the owner-change handler**. Read TARGETS
  immediately, but pull the image bytes themselves lazily/asynchronously and with our
  own receiver window, carefully supporting INCR (transferring large properties in
  chunks). Never block the X loop for the duration of the download.

- **#2523 "Large images are not saved"** + **#2377 "extremely lag …
  hasClipboardFormat('image/png')"** + logs `ELAPSED 576 ms accessing imageData`,
  `Retrying to obtain clipboard`. Large/high-resolution screenshots (Spectacle) make a
  simple format query slow; at 100 MiB CopyQ effectively stuck, and a parallel copy hung
  the application (#2523 comment).
  → We set a **hard size limit** on the captured image (e.g. 24–32 MiB per target;
  configurable). If it's larger — we don't pull the bytes, we show a placeholder element
  "large image, not saved" or simply skip it. The download runs in a separate goroutine
  with a timeout; the popup UI **never** waits. In #2377 a special problem surfaced:
  Spectacle appends garbage **after IEND** in the PNG — don't rely on "size = PNG size",
  read however much is handed over.

- **#3247 / #3375 "High memory usage"**. CopyQ's RAM grows to 1 GB+ over days of idle;
  the in-memory image storage and Qt objects are not freed. Telling: in #3375 "Hide
  tabs" instantly dropped memory from 167 MB to 25 MB — i.e. widgets/decoded previews
  hung in memory for invisible elements.
  → We're entirely in-memory, and we'll **definitely** hit this first if we store
  full-size decoded images. Rules: (a) decode into pixels only the thumbnail, keep the
  original bytes compressed (PNG/JPEG as-is); (b) an overall cap on the total volume of
  the image history (e.g. N MB), with eviction of the oldest; (c) don't create GTK
  widgets/`GdkPixbuf` for elements that aren't currently on screen (lazy rendering in the
  list).

### 2. Format fidelity, transparency, conversion

- **#40 "Alpha channel missing from previews"** — transparency was lost due to the
  priority of BMP over PNG. → For us, PNG is always above BMP; we render the thumbnail
  with alpha (`GdkPixbuf` with alpha), the background under the preview is a
  checkerboard/transparent, not filled white.

- **#2185 "Keeping images as JPEG (not BMP)"** (closed). Copied JPEG → pasted JPEG. But
  after the element was "scrolled" through the history and returned to the clipboard,
  CopyQ handed over **BMP** instead of JPEG. That is, on re-issue it re-encoded from the
  internal QImage.
  → gnome-clipboard-history-native rule: **we hand over to the selection exactly the bytes and the
  target we stored**. No silent re-conversion JPEG→BMP/PNG on re-paste. Internal decoding
  is only for the thumbnail.

- **#2961 "Image quality loss"** (Word: high-quality PNG + low-quality SVG). CopyQ by
  default preferred to render/hand over one format, while the system used another, and
  the quality differed from the native Ctrl+V. The author's reply is important: "CopyQ
  doesn't behave like the system clipboard, and because of that there will be more
  incompatibilities."
  → We should reproduce the system clipboard's behavior as closely as possible: store
  **all** significant image targets the source gave, and on request hand over exactly the
  requested one, not "our favorite".

- **#973 "base64 string item" / #2084 "Images stored as text from browsers"** (48
  comments, open). From luakit/webkit an image arrives as `data:image/png;base64,...` in
  `text/plain`, from chromium/firefox — as `text/html`/`text/plain` with a link, with no
  image target at all. CopyQ has a predefined workaround command: recognize
  `data:...;base64,` and `setData(format, fromBase64(...))`.
  → gnome-clipboard-history-native can build this in natively: if `text/plain` is a `data:image/*;
  base64,`, decode into a real image target and store it as an image. Cheap and local.

- **#2037 "pasting image/gif into browser doesn't work"**, **#2456 "Copy/Paste multiple
  images"**. GIF and multiple images are poorly supported everywhere. For multiple images
  there's no standard X11 target at all (Ditto on Windows glues them into one large
  bitmap — not our path).
  → gnome-clipboard-history-native: one image = one element. An animated GIF we store as bytes and hand
  over as `image/gif`, but we draw the thumbnail from the first frame; we don't promise
  animation. "Paste several images at once" — we don't support (at worst — sequential
  pasting one at a time, like CopyQ's Paste All workaround).

### 3. Handing the image back (paste-back, negotiation)

- **#957 "Emacs cannot copy text if image in clipboard"**. While the selection owner
  (CopyQ) holds an image item, Emacs with `save-interprogram-paste-before-kill` couldn't
  copy text: `Selection owner couldn't convert: STRING`.
  → The selection owner **must correctly answer conversion of all declared targets**,
  including `TARGETS`, `MULTIPLE`, `TIMESTAMP`, and for a request of an unsupported one —
  refuse in the standard way (empty/`None`), not silently. Lesson: don't declare a target
  you can't hand over.

- **#2185 / #2961** (see above) — negotiation: an application requests a format we didn't
  store. We declare in `TARGETS` **only the formats we actually hold**. If we only have
  PNG — we declare only PNG (+ derivatives we're ready to synthesize on request, e.g. BMP
  from PNG — but then we do it honestly and on demand, not by substituting the primary).

- **INCR on handoff**: large images must be handed over in chunks (X11's INCR protocol) —
  otherwise `X_ChangeProperty` on a megabyte-sized property crashes (#1070). Our
  daemon-owner must be able to both **receive** and **hand over** via INCR.

### 4. Thumbnails and UI

- **#3129 "Inconsistent Image Preview Sizes and Duplicated Entries"**. Two bugs at once:
  (a) an image preview from the file manager ignores "Maximum width/height" → giant
  thumbnails break the list layout; (b) a single screenshot creates **two elements**
  (the data + a separate preview).
  → For us: a single fixed thumbnail size in the list (we scale preserving aspect ratio,
  `GdkPixbuf.scale_simple`/`gdk_pixbuf_new_from_stream_at_scale`). One copy event = one
  history element (see dedup below), no duplicates.

- **#3499 "thumbnail toggle" / #3577 "Scale images to fit preview area"**. People want
  uniform row height (like in Ditto) and a preview fitted to the area, not a 100% zoom.
  → We do it right away: list rows of uniform height, the thumbnail `contain` (by the
  larger side), the large preview also fitted into the panel, we don't show a center crop
  of a large image.

- **#252 "Save to file" (45 comments) / #1616 "save as jpg" / #2840 "Resize and
  paste"**. Mass demand: save an image from the history to a file, paste with a resize.
  In CopyQ this is only via user scripts.
  → For gnome-clipboard-history-native, "Save image as…" from the popup context menu is a cheap and
  very desirable feature. Resize on paste — optional, later.

### 5. Application specifics

- **Browsers (Chrome/Firefox/Edge/Vivaldi/Brave)** — #2084, #2046, #1591, #1100, #2936:
  "Copy image" often puts only `text/html`/`text/plain` (a link), sometimes without an
  image target at all. Firefox more often gives a normal `image/png`, chromium-like ones
  — more often only HTML. `.webp` from the browser may not render at all (#2936). → Don't
  count on "Copy image" = there's an image in the clipboard. Check for the presence of a
  real image target; if there's none — treat the element as text/link, without a fake
  preview.
- **GNOME Screenshot / Spectacle / Flameshot** — give a normal `image/png`, this is our
  "good" case. But Spectacle may append data after IEND (#2377) and produce very large
  PNGs (lags).
- **GIMP** — crashed due to the INCR conflict (#1070). Care with large transfers is
  critical.
- **LibreOffice / MS Office / PowerPoint** — #2961, #3555, #2068: put several formats
  (PNG + SVG + their own), choosing "the wrong one" breaks quality or pastes the wrong
  thing. We hand over the requested format, not our favorite.
- **Nautilus (copy of an image file)** — `text/uri-list` +
  `x-special/gnome-copied-files`, no image data. A local file we read ourselves for the
  preview.

---

## Recommendations for the implementation in gnome-clipboard-history-native

### What to capture

On a change of the CLIPBOARD owner we request `TARGETS`. Then by priority:

1. There's `image/png` / `image/svg+xml` / `image/jpeg` / `image/tiff` / `image/bmp`
   / `image/gif` → this is an **image**. We pull the bytes of the chosen primary target
   (PNG preferred) asynchronously, with a size limit and INCR support.
2. No image target, but `text/plain` = `data:image/*;base64,...` → we decode into an
   image element (#973).
3. No image target, but `text/uri-list`/`x-special/gnome-copied-files` points to a
   **local** image file → we read the file from disk for the preview; on paste we hand
   over the uri-list as-is (this is a "paste file" operation, not "paste pixels").
4. Only `text/html`/`text/plain` with a link to a remote image → this is **text**, not
   an image. We don't download remote content (#1591, #2084).

### Image key/dedup

`hash = sha256(primary_target_bytes)`. Dedup — by the hash: re-copying the same image
doesn't create a duplicate (fixes "Duplicated Entries" #3129) but raises the existing
element to the top. We compute the hash from the **compressed** bytes (not the decoded
pixels) — cheap and stable.

### Thumbnails in GTK

- We decode straight to the reduced size:
  `gdk_pixbuf_new_from_stream_at_scale(stream, max_w, max_h, preserve_aspect=TRUE)`
  — we don't load a full-size pixbuf into memory for the sake of a list icon.
- Fixed row height; thumbnail `contain`, with alpha (checkerboard background).
- Lazy rendering: we create the thumbnail pixbuf for visible rows, release it on scroll
  (memory — see #3247/#3375).
- The full-size preview (on hover/selection) — also fitted into the panel (#3577), and we
  keep a full-size pixbuf only for the one current element.

### Memory limit

- A cap on a single capture (e.g. 24–32 MiB per target) — beyond that we don't pull
  (#2523).
- A cap on the total volume of the image history (e.g. 128–256 MiB) with LRU eviction.
- We store the **compressed original bytes**, not decoded RGBA. We decode only the
  thumbnail and only the current preview.

### Handing the image over as X11 selection owner

- Our resident daemon owns CLIPBOARD. In `TARGETS` we declare **only** the actually
  stored image targets + the service ones (`TARGETS`, `TIMESTAMP`, `MULTIPLE`) (#957 —
  otherwise we break other applications' copies).
- On `SelectionRequest` we hand over the **stored bytes of the requested target without
  re-conversion** (#2185, #2961). If we can synthesize (e.g. BMP from PNG) — only on an
  explicit request, we don't substitute the primary.
- Large data — via INCR (#1070). We implement both receiving and handing over in chunks.
- After the XTEST paste we continue to own the selection (we don't release it
  immediately) while the receiving application finishes reading the data (especially with
  INCR).

### Terminal-aware pasting

Images in terminals are meaningless. If the target window (where we'll paste via XTEST)
is a terminal (by WM_CLASS: gnome-terminal, xterm, kitty, alacritty, konsole, etc.), then
for an image element:

- either we **disable** the image paste (item inactive / grayed out),
- or we paste **fallback text** (e.g. the path to the saved temp file, if "save to disk"
  mode is enabled), but by default — just skip with a hint.

This same rule guards against an XTEST paste of binary bytes into a TTY.

---

## Open questions

- **Store to disk?** Today gnome-clipboard-history-native is in-memory. Images will quickly hit the
  memory ceiling (#3247/#3375). Perhaps image elements need optional disk-backing (temp
  files), which would then easily solve both "Save as" and "paste into terminal by path"
  — but that's a departure from the in-memory principle. To decide.
- **On-demand format synthesis.** Should we hand over BMP/JPEG if we store only PNG (for
  applications that ask strictly for BMP)? It's convenient, but risks repeating #2961 (a
  quality mismatch). Hand over only on an explicit request?
- **The exact size limit** for a single capture and the total image history — tune
  empirically on real 4K screenshots.
- **INCR** — how fully to support it in the first version? The minimum for receiving
  large PNGs is needed right away; handing over via INCR too (otherwise we won't paste a
  large screenshot into GIMP, #1070).
- **`.webp` / `.avif`** — should we render a thumbnail (needs a loader in GdkPixbuf)?
  CopyQ tripped on webp (#2936). Check that the Yaru/GTK stack can decode them,
  otherwise — a placeholder icon.
- **uri-list to an image**: show a file preview — yes, but on paste hand over the pixels
  or the file itself (uri-list)? Probably leave it as-is (a file operation), not turning
  it into an image-paste.
