# Remote access with Tailscale (increment 2)

Why Tailscale: see [ADR-0003](adr/0003-tailscale-remote-access.md). Nothing is exposed to the public
internet; the phone and the Mac mini join the same private network ("tailnet") and talk directly.

## One-time setup

### Mac mini

1. Install the app (needs an admin password, so run it yourself, not from a script):

   ```sh
   brew install --cask tailscale-app
   ```

2. Open **Tailscale** from Applications, log in (Google/GitHub/Microsoft/Apple account or email) and
   allow the VPN configuration when macOS asks. Tailscale then starts at login and keeps running in
   the menu bar.

3. Make the CLI available in the terminal (the app bundles it):

   ```sh
   sudo ln -s /Applications/Tailscale.app/Contents/MacOS/Tailscale /usr/local/bin/tailscale
   tailscale status
   ```

4. In the Tailscale admin console (<https://login.tailscale.com/admin/dns>) enable **MagicDNS** and
   **HTTPS certificates**. HTTPS is needed later for the PWA, push notifications and microphone.

### Android phone

1. Install **Tailscale** from the Play Store and log in with the same account.
2. Toggle the VPN on. The Mac mini should appear in the machine list.

## Verify (increment 2 check)

On the phone with **Wi‑Fi off** (mobile data only) and Tailscale on, open

```
http://<mac-hostname>:1984
```

where `<mac-hostname>` is the MagicDNS name shown by `tailscale status` on the Mac (for example
`macmini`). The go2rtc page must load and the `dog` stream must play with audio.

## Notes

- Tailscale names and IPs (`100.x.y.z`) are private to your tailnet; they are safe to put in config
  files but are still kept out of git by convention.
- go2rtc binding to `:1984` on all interfaces is temporary (increment 1 proof). From increment 3 on,
  go2rtc listens on localhost only and the Go service is the only thing reachable over the tailnet.
- `tailscale serve` (increment 5) will put a valid HTTPS certificate in front of the Go service.

## HTTPS with `tailscale serve` (increment 5)

The watchmydog service listens on `127.0.0.1:8080` only. `tailscale serve` publishes it on the
tailnet with a valid Let's Encrypt certificate:

```sh
make serve        # tailscale serve --bg http://127.0.0.1:8080
```

The app is then at `https://<mac-hostname>.<tailnet>.ts.net` (here
`https://maikos-mac-mini.tail54b4a7.ts.net`). The setting persists across reboots; `make serve-off`
removes it. This is `serve` (tailnet only), not `funnel` (public internet).

HTTPS is required for the login cookie (`Secure`), and later for the installable app, push
notifications and the microphone (two-way audio).
