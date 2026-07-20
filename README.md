# shgterm

`shgterm` is a terminal-based USI-to-CSA bridge for shogi engines. It connects a
local USI engine to a CSA protocol game server and renders the running game in
the terminal.

## Requirements

- Go 1.26 or newer
- A USI-compatible shogi engine
- CSA server credentials

## Build

From the repository root, run:

```bash
go build -o shgterm ./cmd/shgterm
```

## Configuration

Start from the sample configuration:

```bash
cp examples/config.sample.yaml config.yaml
```

Edit `config.yaml` to point at your USI engine and CSA server account.

## Run

```bash
./shgterm config.yaml
```

To list configured servers:

```bash
./shgterm --list-servers config.yaml
```
