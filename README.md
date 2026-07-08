# teleport

P2P clipboard sync between Windows and macOS over LAN. Copy on one device, paste on another.

## Features

- **Text & files** — copy text or files, paste on the other device
- **Encrypted** — AES-256-GCM with scrypt key derivation, unique session key per connection
- **Zero config** — auto-discovery via UDP multicast, or direct connect with `-peer`
- **No server** — peer-to-peer, everything stays on your local network
- **VPN bypass** — `-bypass-vpn` flag adds a direct LAN route (requires admin)

## Quick Start

**Windows:**
```bash
go build -o teleport.exe ./cmd/teleport
teleport.exe -pass "my-secret-password" -peer 192.168.0.221:9878
```

**macOS:**
```bash
CGO_ENABLED=1 go build -o teleport ./cmd/teleport
./teleport -pass "my-secret-password" -peer 192.168.0.137:9878
```

Both devices must use the same password. Copy text or files on one — paste on the other.

## Install

### Build from source

Requires Go 1.25+. macOS also needs Xcode Command Line Tools (`xcode-select --install`).

```bash
git clone https://github.com/alexnobleburn/teleport.git
cd teleport

# Windows
go build -o teleport.exe ./cmd/teleport

# macOS
CGO_ENABLED=1 go build -o teleport ./cmd/teleport
```

### Download from GitHub Actions

1. Go to [Actions](https://github.com/alexnobleburn/teleport/actions)
2. Click the latest green run
3. Download artifact: `teleport-Windows` or `teleport-macOS`

## Usage

```
teleport -pass <password> [options]
```

| Flag | Default | Description |
|------|---------|-------------|
| `-pass` | — | Encryption password (required, or `TELEPORT_PASS` env) |
| `-name` | hostname | Device name |
| `-peer` | — | Direct connect to host:port (skip auto-discovery) |
| `-port` | 9878 | TCP listen port |
| `-bypass-vpn` | false | Add direct LAN route bypassing VPN (requires admin/sudo) |
| `-text-only` | false | Sync text only, skip files |
| `-verbose` | false | Debug logging |
| `-poll-interval` | 300ms | Clipboard poll interval (macOS only) |
| `-log-json` | false | JSON log format |

### Password via environment variable

```bash
# Password won't appear in process list
export TELEPORT_PASS="my-secret-password"
./teleport -peer 192.168.0.137:9878
```

### VPN bypass

When VPN redirects local traffic, use `-bypass-vpn` to add a direct route:

```bash
# Windows (run as Administrator)
teleport.exe -pass "secret" -peer 192.168.0.221:9878 -bypass-vpn

# macOS (sudo for route add)
sudo ./teleport -pass "secret" -peer 192.168.0.137:9878 -bypass-vpn
```

## How It Works

```
┌─────────────┐          encrypted TCP          ┌─────────────┐
│  Windows 11 │ ◄──────────────────────────────► │    macOS     │
│             │    AES-256-GCM + scrypt KDF      │             │
│ Ctrl+C copy │                                  │ Cmd+V paste │
│ clipboard   │    UDP multicast discovery       │ clipboard   │
│ monitoring  │    or direct -peer connect       │ polling     │
└─────────────┘                                  └─────────────┘
```

1. App monitors system clipboard for changes
2. On copy: reads content, sends encrypted to peer
3. Peer receives, decrypts, puts into local clipboard
4. User pastes normally (Ctrl+V / Cmd+V)

Files are transferred eagerly (at copy time) and saved to `~/.teleport/staged/` before being placed in the clipboard as file references.

## Network Requirements

| Protocol | Port | Purpose |
|----------|------|---------|
| UDP | 9877 | Auto-discovery (multicast 239.255.77.55) |
| TCP | 9878 | Data transfer (encrypted) |

Auto-discovery may not work on corporate WiFi. Use `-peer host:port` for direct connection.

## Security

- **AES-256-GCM** encryption on all traffic
- **scrypt** key derivation (N=65536, r=8, p=1) — resistant to brute force
- **HKDF** session key per TCP connection — nonce reuse impossible even on reconnect
- **Handshake** rejects wrong passwords without leaking information

**Warning:** All clipboard contents are synced, including passwords from password managers. Use `-text-only` to limit to text only.

## License

MIT
