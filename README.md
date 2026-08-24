# Axis

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

Axis exposes JSON APIs for projects, sessions, and runs. It invokes AX as a subprocess and streams JSONL events to clients with Server-Sent Events. It has no compile-time dependency on AX and serves no user interface.

Keep Axis on a private network even with authentication.

## Test

```sh
go test ./...
```
