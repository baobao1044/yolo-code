# Quickstart

## Install

```bash
go install github.com/yolo-code/yolo/cmd/yolo@latest
```

Or clone and build:

```bash
git clone https://github.com/baobao1044/yolo-code.git
cd yolo-code
go build ./...
```

## First headless run

```bash
echo "explain the main function" | yolo --headless
```

The output is one JSON line per event. The transcript is deterministic for the
same input, so it can be replayed or hashed for regression tests.

## Interactive run

```bash
yolo
```

Type a task at the bottom prompt and press Enter. The board shows multi-agent
progress when the coordination layer decomposes the task.

## Update checks

```bash
yolo version
```
