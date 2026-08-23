# Axo

Interactive web client for AX.

## Run

```sh
export AXO_ADDRESS=127.0.0.1:8081
export AXO_AX_PATH=ax
export AXO_USERNAME=axbot
export AXO_PASSWORD=change-me
axo
```

Axo stores session metadata and AX JSONL sessions under `~/.local/share/axo` by default. Set `AXO_DATA_DIR` to change it.

Axo invokes AX as a subprocess and streams its JSONL events to the browser with Server-Sent Events. It has no compile-time dependency on AX.

Keep Axo on a private network even with authentication.

## Test

```sh
go test ./...
```
