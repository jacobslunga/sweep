# Sweep

A small macOS disk cleaner with a cached, expandable file tree. Written in Go with Bubble Tea, Bubbles, and Lip Gloss.

## What it does

- Browse a compact file tree, sorted by size, name, or modification time.
- Expand folders, filter files, and inspect full paths without rescanning.
- Press **d** to delete an item after a simple confirmation.
- Keep browsing while deletions run in the background; quit waits for them to finish.
- Use `--sudo` when administrator permissions are needed.

## Requirements

- **macOS** — currently uses macOS filesystem APIs; Linux and Windows are not supported.
- **Homebrew** for the easiest installation, or download a prebuilt binary.
- **Go 1.26.2 or newer** and Git only if building from source.
- An interactive terminal at least **45 columns × 18 rows**. No special font is needed.

Apple Silicon is tested locally. Intel downloads are cross-compiled and have not been tested on Intel hardware.

## Install with Homebrew

```sh
brew tap jacobslunga/sweep https://github.com/jacobslunga/sweep
brew install jacobslunga/sweep/sweep
sweep
```

This uses the project's custom tap, not Homebrew's main catalog. If your Homebrew version requests trust, approve this formula. Updates are installed with:

```sh
brew update
brew upgrade jacobslunga/sweep/sweep
```

## Download a binary

Download an archive and `checksums.txt` from the [latest release](https://github.com/jacobslunga/sweep/releases/latest): `darwin_arm64` is for Apple Silicon; `darwin_amd64` is for Intel. No Go installation is needed.

For example, for Apple Silicon v0.3.1, from the folder containing the downloaded files:

```sh
shasum -a 256 --ignore-missing -c checksums.txt
tar -xzf sweep_0.3.1_darwin_arm64.tar.gz
mkdir -p "$HOME/.local/bin"
install -m 755 sweep "$HOME/.local/bin/sweep"
```

Verify the matching archive reports **OK** before extracting it. The binaries are not Apple Developer ID signed or notarized; macOS may ask you to approve direct downloads. See the PATH instructions below if `sweep` is not found.

## Run from source

Clone the repository and run directly from source:

```sh
git clone https://github.com/jacobslunga/sweep.git
cd sweep
go run . ~/Downloads
```

Go downloads the dependencies on the first run. Omit the path to scan your home folder:

```sh
go run .
```

Prefer SSH? Use `git clone git@github.com:jacobslunga/sweep.git` instead.

## Install locally

Build a native binary and put it on your PATH:

```sh
go build -trimpath -ldflags='-s -w' -o bin/sweep .
mkdir -p "$HOME/.local/bin"
install -m 755 bin/sweep "$HOME/.local/bin/sweep"
```

If `sweep` is not found, add this line to `~/.zshrc` (or your shell's startup file), then open a new terminal:

```sh
export PATH="$HOME/.local/bin:$PATH"
```

Verify the install:

```sh
sweep --version
sweep --help
```

To update later, run `git pull --ff-only` in your checkout, then repeat the build and install commands. To uninstall, remove `~/.local/bin/sweep`; Sweep does not install a service or save an on-disk file index.

## Usage

```sh
sweep                 # start in your home folder
sweep ~/Downloads     # start in a specific folder
sweep --sudo /Library  # administrator access; sudo prompts for your password
sweep --version
```

The header shows the version, system architecture, scan directory, and disk capacity. The tree shows file and folder sizes and modification dates.

| Key               | Action                                                   |
| ----------------- | -------------------------------------------------------- |
| ↑ ↓ / j k         | Move through the tree                                    |
| Enter / Space     | Expand or collapse a folder                              |
| → / l             | Expand or move into a folder                             |
| ← / h / Backspace | Collapse or move to the parent                           |
| d                 | Ask to delete the highlighted file or folder             |
| y                 | Confirm deletion while the popup is open                 |
| Esc / n           | Cancel deletion                                          |
| Tab / ← / →       | Choose Cancel or Delete in the popup; Enter activates it |
| /                 | Filter the tree, including collapsed descendants         |
| s                 | Sort by size, modification time, or name                 |
| g                 | Choose a folder                                          |
| r                 | Explicitly refresh the scan                              |
| i                 | Full path and details                                    |
| e / ?             | Scan errors / help                                       |
| q / Ctrl+C        | Quit after all background deletions finish               |

Click a row to focus it; click the focused folder to expand or collapse it. The mouse wheel scrolls. No special font is required.

## Cached tree

Sweep scans at startup and keeps the tree in memory for the session. Expanding, collapsing, filtering, opening details, dismissing dialogs, and deleting items do not rescan the root. Confirming deletion immediately removes the cached subtree and updates ancestor sizes. Each deletion runs in its own background goroutine, so you can keep browsing and delete other items. Your expanded folders and scroll position are retained where possible. If an operation fails, its remaining files reappear in the tree and an error is reported.

Press **r** to discover changes made by other apps. Refresh and folder-change commands are unavailable while deletions are pending; retry when they finish. Choosing a folder already in the tree navigates to it using the cache; choosing an unscanned location starts a scan there. The cache is not saved between app launches.

## Deletion

Press **d** on an item, then **y**, or choose **Delete** and press Enter. The popup defaults to Cancel. Deletion is permanent; there is no undo.

Sweep revalidates the selected item immediately before deletion. Directories require a targeted check of their contents, but this does not rescan the whole root. If deletion fails, only the affected target is refreshed so partial changes are reflected accurately. A parent folder cannot be deleted while a child deletion is still pending.

Only the filesystem root, scan root, home directory, and ancestors of the home directory are protected. There is no blanket block on system or cache directories. Symlinks are not traversed. Incomplete scans cannot be deleted. A directory containing new, changed, or missing files is rejected until its cached state has been refreshed. Deletion is not transactional: an error can leave some reviewed children removed.

**Ctrl+C**, **q**, SIGINT, and SIGTERM stop accepting new work and wait for every pending deletion to finish. The status line shows how many deletions remain. Repeated Ctrl+C does not interrupt this wait. Failed operations are also printed after the terminal UI exits.

Sizes are logical bytes. APFS clones, snapshots, hard links, and sparse files can make actual reclaimed space differ. Disk capacity in the header reflects the volume's current free space. Cache files may be in use; close their owning apps before deleting them.

Use `sweep --sudo /Library` to scan with administrator permissions and delete items such as `/Library/Caches`. The header identifies administrator mode. Sweep uses the standard sudo prompt and never handles or stores your password. Launch at the parent directory to delete a whole folder: the scan root itself stays protected. macOS can still deny access to protected locations; errors are reported. No telemetry or background service.

## Local development

From the repository root:

```sh
go mod download
go run . ~/Downloads
```

For an isolated playground, create a disposable folder and launch Sweep there:

```sh
sweep_demo_dir="$(mktemp -d /tmp/sweep-demo.XXXXXX)"
mkdir -p "$sweep_demo_dir/example/nested"
printf 'Try deleting this file.\n' > "$sweep_demo_dir/example/note.txt"
printf 'Another test file.\n' > "$sweep_demo_dir/example/nested/another.txt"
go run . "$sweep_demo_dir"
```

Run the checks before submitting a change:

```sh
gofmt -w *.go
go test -race ./...
go vet ./...
go build -trimpath -ldflags='-s -w' -o bin/sweep .
```

Tests use disposable fixtures, covering filesystem validation, symlinks, cancellation, tree navigation, popup confirmation, incremental cache updates, consecutive deletions, mouse input, terminal resizing, concurrent jobs, failure restoration, and graceful shutdown. They do not require sudo or delete your own files.

### Project layout

| File | Purpose |
| --- | --- |
| `main.go` | CLI flags, sudo launch, and shutdown handling |
| `tui.go` | Tree, dialogs, cached state, and background jobs |
| `filesystem.go` | Scanning, target validation, and deletion |
| `platform.go` | Platform-specific directory access |
| `*_test.go` | Filesystem and interface tests |

### Troubleshooting

- **Permission denied:** for administrator access, build or install the binary, then run `sweep --sudo /Library`. macOS privacy protections may still limit access.
- **Cannot delete the top-level folder:** start Sweep at its parent. The current scan root is protected.
- **Files changed outside Sweep:** press **r** to refresh the session cache.
- **Interactive terminal required:** run Sweep directly in a terminal, without piping its input.
- **Quit is taking a while:** Sweep is finishing pending deletions. The status line shows how many remain.

## Contributing

Open an issue or pull request with a description of the problem and how to reproduce it. For behavior changes, include a test using temporary files and run the checks above. Please keep the interface focused on browsing and cleaning files.

## Releases

See [Publishing Sweep](docs/RELEASING.md) for the versioning, packaging, tagging, and Homebrew update process.
