# Installation

Wiregram is one static binary with no runtime dependencies. Releases are built for
Linux, macOS and Windows on amd64 and arm64.

## Binary

Download the [latest release](https://github.com/lebmatter/wiregram/releases/latest)
for your platform. The assets are named `wiregram-OS-ARCH`:

| Platform | Asset |
|----------|-------|
| Linux x86-64 | `wiregram-linux-amd64` |
| Linux ARM64 | `wiregram-linux-arm64` |
| macOS Intel | `wiregram-darwin-amd64` |
| macOS Apple silicon | `wiregram-darwin-arm64` |
| Windows x86-64 | `wiregram-windows-amd64.exe` |
| Windows ARM64 | `wiregram-windows-arm64.exe` |

```sh
curl -fLo wiregram https://github.com/lebmatter/wiregram/releases/latest/download/wiregram-linux-amd64
chmod +x wiregram
./wiregram
```

Each release has a `checksums.txt` with the SHA-256 of every asset. Check the download
before running it:

```sh
curl -fLO https://github.com/lebmatter/wiregram/releases/latest/download/checksums.txt
sha256sum --ignore-missing -c checksums.txt
```

## Windows

Run it from [Windows Terminal](https://aka.ms/terminal), which has the truecolor and
mouse support Wiregram needs. In PowerShell:

```powershell
Invoke-WebRequest https://github.com/lebmatter/wiregram/releases/latest/download/wiregram-windows-amd64.exe -OutFile wiregram.exe
Get-FileHash wiregram.exe      # compare with checksums.txt
.\wiregram.exe
```

The binary is not code-signed, so SmartScreen may warn on first run. Settings and data
live in `%AppData%\wiregram`.

## Linux packages

Releases also carry `.deb` and `.rpm` packages that install `/usr/bin/wiregram`:

```sh
sudo apt install ./wiregram-linux-amd64.deb     # Debian, Ubuntu
sudo dnf install ./wiregram-linux-amd64.rpm     # Fedora, RHEL
```

## From source

Requires Go 1.26 or newer. No C compiler is needed.

```sh
git clone https://github.com/lebmatter/wiregram
cd wiregram
make
./wiregram
```

See [Building](/for-developers/building) for embedding Telegram and Google credentials
in your own build.

## Optional tools

- **[mpv](https://mpv.io)** plays audio and video from the chat.
- **[kitty](https://sw.kovidgoyal.net/kitty/) or [Ghostty](https://ghostty.org)** draws
  images at full resolution. Other truecolor terminals get half-block images.
- **`xdg-open`** (Linux) or **`open`** (macOS) opens attachments in your default app;
  Windows needs nothing extra. On Windows, `mpv.exe` must be on `PATH`.

## First launch

Run `wiregram` in an interactive terminal. After the splash, the first launch opens
**Add account**; pick a network and follow its [setup guide](/setup-guides/telegram).
Run `wiregram -h` for the full shortcut list.
