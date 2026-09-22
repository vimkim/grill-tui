# grill-tui

A terminal worksheet for answering a long run of numbered questions quickly, then pasting
the whole set back into the conversation that asked them.

When an AI grills you with twenty numbered questions, answering them in the chat box is
miserable. You lose your place, you retype the same three answers, and the numbering drifts.
`grill-tui` gives you a numbered grid, one-keystroke answers for the common cases, and a
single key that copies every answer you gave, correctly numbered, straight to your clipboard.

## Install

If you just want the tool:

```sh
go install github.com/vimkim/grill-tui/cmd/grill-tui@latest
```

If you want the source too:

```sh
git clone https://github.com/vimkim/grill-tui
cd grill-tui
just install
```

Both routes put the binary wherever your Go installation sends binaries, which is `GOBIN`
if you have set it and `GOPATH/bin` otherwise. The `just install` route prints the exact
destination, which is useful the first time, since that directory is not always on `PATH`.

## Usage

Run it in the directory you are working in:

```sh
grill-tui
```

The first run asks for a starting number, so the worksheet lines up with the question the
AI just asked. Pass it directly to skip the prompt:

```sh
grill-tui 12
```

The worksheet belongs to the directory you started it from, so running it again in the same
place picks up exactly where you left off. A supplied starting number is ignored once a
worksheet exists, which means an absent-minded `grill-tui 1` cannot wipe your answers.

You get ten numbered slots to begin with. Answer the last one and another appears, so a
long interview never runs out of room.

## Keys

| Key | What it does |
| --- | --- |
| `↑` `↓`, `j` `k`, `Ctrl-N` `Ctrl-P` | Move between slots |
| `Space` | Skip without answering |
| `r` `y` `n` | Answer `recommended`, `yes`, `no` |
| `1`-`5` | Answer with that number |
| `a`-`e` | Answer with that letter, keeping the case you typed |
| `x` | Answer `explain further` |
| `i` | Write a custom answer inline |
| `o` | Write a custom answer in your editor |
| `Esc` | Clear the selected answer |
| `u` | Undo the last change |
| `s` or `Ctrl-S` | Copy every answer to the clipboard |
| `?` | Full help |
| `q`, `Ctrl-Q`, `Ctrl-C` | Quit |

Every answer advances to the next slot, so you can hold a rhythm without reaching for the
arrow keys. Capital letters work wherever lowercase does. The mouse works too: click to
select a slot, scroll to move through them.

Long answers are truncated in the grid so it stays scannable, and the selected answer is
always shown in full underneath.

## Copying your answers

`s` copies only the slots you actually answered, each one numbered, with continuation lines
of multi-line answers indented to line up under the first. Paste it back into the
conversation and the numbering matches the questions.

`Ctrl-S` does the same thing, but some terminals swallow it for flow control, which is why
plain `s` is the one to reach for.

The clipboard is found automatically. On WSL it uses `clip.exe`, on Wayland `wl-copy`, and
on X11 `xclip` or `xsel`. If none of those are available, which is the normal situation over
SSH, it falls back to OSC 52 and asks the terminal itself to take the text. The status line
names whichever one worked.

## Your editor

`o` opens the selected answer in your editor, which is `$VISUAL` if set and `$EDITOR`
otherwise. Save and quit to commit the answer. The value is treated as a single executable
path, so if you need flags, point it at a small wrapper script.

## Where things are kept

Answers live in `.grill-tui/worksheet.json`, in the directory you ran the tool from, written
so only you can read it. Delete that directory to start over.

Colors are used where the terminal supports them. Set `NO_COLOR` to turn them off.

## Development

The project uses [just](https://github.com/casey/just):

```sh
just           # list the recipes
just run 12    # run from source, with an optional starting number
just check     # formatting, vet, and the full test suite
just build     # build into .tmp/
just install   # install from this source tree
```

`just check` is the gate to run before pushing. The test suite drives the real binary
through a pseudo-terminal, so it exercises the keys as a terminal actually delivers them.
