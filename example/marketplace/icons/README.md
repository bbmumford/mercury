# Mercury Marketplace PWA Icons

The `manifest.json` expects the following icon files to be present
in this directory. They are **not committed** because we don't have
source branding artwork — supply your own before deploying.

| Filename              | Size      | Purpose              | Notes                                                    |
| --------------------- | --------- | -------------------- | -------------------------------------------------------- |
| `icon-192.png`        | 192 x 192 | Standard app icon    | Used on Android home screens, browser tabs.              |
| `icon-512.png`        | 512 x 512 | Large app icon       | Used for splash screens and install prompts.             |
| `icon-maskable.png`   | 512 x 512 | Maskable app icon    | Keep important content inside the inner 80% safe zone.   |

## Generating icons

Recommended tool: <https://realfavicongenerator.net/>

1. Upload a single high-resolution square source image (ideally 1024 x 1024 PNG with transparent background).
2. Under the Android / PWA section, set the theme colour to `#121212` and background to `#121212` to match `manifest.json`.
3. For the maskable icon, make sure your artwork sits within the inner safe zone (40% radius).
4. Download the generated archive and copy the three files above into this folder.

Alternative CLI tools:
- [`pwa-asset-generator`](https://github.com/onderceylan/pwa-asset-generator)
- [`sharp-cli`](https://github.com/vseventer/sharp-cli) to resize manually

## Verifying

After adding the icons, open the marketplace in Chrome DevTools →
Application → Manifest. All three icons should load without errors
and the "Add to Home Screen" install prompt should become available
after a couple of visits.
