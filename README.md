# Axis

<p align="center"><img src=".github/ax.svg" width="96" height="96" alt="AX ecosystem"></p>

API server for AX clients.

## Run

```sh
export AXIS_ADDRESS=127.0.0.1:8081
export AXIS_AX_PATH=ax
export AXIS_USERNAME=axbot
export AXIS_PASSWORD=change-me
axis
```

Axis stores session metadata and AX JSONL sessions under `~/.local/share/axis` by default. Set `AXIS_DATA_DIR` to change it.

Projects map names and IDs to server-side working directories. Configure them in `~/.config/axis/projects.json`, or set `AXIS_PROJECTS_FILE`:

```json
{
  "roots": [
    {
      "name": "Code",
      "path": "/home/user/Code"
    }
  ],
  "projects": [
    {
      "id": "ax-ecosystem",
      "name": "AX ecosystem",
      "path": "/home/user/Code/ax-ecosystem"
    }
  ]
}
```

Roots limit the directories visible in Axi's remote directory picker. Axi can register directories inside those roots as projects. When the file does not exist, Axis uses the server user's home directory as the browse root and creates a default in-memory project for `AXIS_PROJECT_PATH` or its current working directory. The first project added through Axi writes the config file.

Axis starts each AX run with `-C` set to its session's project path.

## Bots

A bot defines a system prompt, model, tools, skill root, and persistent workspace. Configure bots through Axi or the `/api/bots` API. Axis stores them in `~/.config/axis/bots.json` by default. Set `AXIS_BOTS_FILE` to change it.

Bot workspace and skill paths must be inside a configured root. Tools must be listed in the Axis process's `AX_TOOLS` value. Every session may reference one bot; changes to a bot apply to its existing chats on their next run. All chats using a bot share its workspace.

Store per-bot environment variables in `~/.config/axis/bots/<bot-id>.env`, or set `AXIS_BOT_ENV_DIR` to change the directory. Axis reads the file only when launching that bot. Files must have mode `0600`. Use one `KEY=value` per line; JSON-style quoted values are supported. Bot files cannot override Axis-controlled tool, workspace, skill, or artifact variables.

## Artifacts

Attachx copies workspace files into session artifact storage. Axis emits `artifact` run events and serves authenticated downloads from `/api/sessions/{session}/artifacts/{artifact}`. Artifacts persist with the chat and are deleted with it.

## Interfaces

Axis supervises enabled interface connectors such as Slaxi. Connector definitions are stored in `~/.config/axis/connectors.json`. Connector secrets belong in mode-`0600` files under `~/.config/axis/connectors/<connector-id>.env`. Set `AXIS_CONNECTORS_FILE`, `AXIS_CONNECTOR_ENV_DIR`, or `AXIS_CONNECTOR_URL` to override those defaults.

Enabled connectors start with Axis, restart after failure with a bounded delay, and stop with Axis. Axis supports one active instance; running multiple replicas would duplicate connector processes.

Axis exposes JSON APIs for projects, sessions, and runs. It invokes AX as a subprocess and streams JSONL events to clients with Server-Sent Events. It has no compile-time dependency on AX and serves no user interface.

Keep Axis on a private network even with authentication.

## Test

```sh
go test ./...
```
