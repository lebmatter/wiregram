# Media

## Inline images

Still images draw inline in a rounded card titled with the media label, with the caption
inside under the image, in the chat and in the thread pane.

| Terminal | How images draw |
|----------|-----------------|
| kitty, Ghostty (outside tmux) | Full-resolution kitty graphics |
| Any other truecolor terminal | Truecolor `▄` half-blocks |

Kitty graphics are detected from `TERM`, `TERM_PROGRAM` and `KITTY_WINDOW_ID`. Inside
tmux, images fall back to half-blocks.

Only images for messages on screen are downloaded. View-once and self-destructing media
never load inline.

## Videos and other files

Videos, video notes and GIFs get a small card with a colour play-button icon beside their details.
Nothing is downloaded until you open it.

Other attachments show as dim tags, such as:

```
[photo] 1280x720, 245 KB
[file] report.pdf, 1.2 MB — the caption
```

## Opening attachments

- **Click** an image card or `[tag]` row to download it and open it in your default app
  with `xdg-open` (`open` on macOS).
- Or select the message and choose **open**, **play** or **save** in its actions.
  Audio and video play in [mpv](https://mpv.io); <kbd>Esc</kbd> returns to the chat.

## Cache

Downloads live in `~/.cache/wiregram/media` (`os.UserCacheDir()/wiregram/media`),
shared by inline images and click-to-open, and are pruned after 7 days unseen.

## Uploading

<kbd>Ctrl+U</kbd> in a chat opens the upload browser. Browse folders with the arrows,
or press <kbd>/</kbd> or <kbd>~</kbd> to type a path (<kbd>Tab</kbd> completes,
<kbd>↑</kbd>/<kbd>↓</kbd> cycle matches). Add an optional caption and press
<kbd>Enter</kbd>.
