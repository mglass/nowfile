# Installing plan

`plan` sets your nowfile Status from a terminal and reads anyone's nowfile over finger. Each release on GitHub carries a binary for Apple Silicon Macs (`plan-darwin-arm64`), one for Intel Macs (`plan-darwin-amd64`), and a `SHA256SUMS` file. Linux and Windows builds come later; until then use `go install`.

## Mac, from a release

Download the binary for your Mac and the checksums:

```sh
ARCH=$(uname -m); [ "$ARCH" = x86_64 ] && ARCH=amd64
curl -fLO "https://github.com/mglass/nowfile/releases/latest/download/plan-darwin-$ARCH"
curl -fLO "https://github.com/mglass/nowfile/releases/latest/download/SHA256SUMS"
```

`uname -m` prints `arm64` on Apple Silicon and `x86_64` on Intel. For a specific version, replace `latest/download` with `download/v0.1.0`.

Check the download against the checksum. This must print `plan-darwin-arm64: OK` (or `amd64`):

```sh
grep "plan-darwin-$ARCH" SHA256SUMS | shasum -a 256 -c
```

Make it executable and put it on your PATH, either system-wide:

```sh
chmod +x "plan-darwin-$ARCH"
sudo mkdir -p /usr/local/bin
sudo mv "plan-darwin-$ARCH" /usr/local/bin/plan
```

or in your home directory, with no sudo:

```sh
chmod +x "plan-darwin-$ARCH"
mkdir -p ~/bin
mv "plan-darwin-$ARCH" ~/bin/plan
```

If you use `~/bin`, make sure it is on your PATH, for example with `echo 'export PATH="$HOME/bin:$PATH"' >> ~/.zshrc` and a new terminal.

Check it runs:

```sh
plan --version
```

This prints the release tag, such as `v0.1.0`.

### Gatekeeper

The binaries are not yet signed or notarized; that comes later. `curl` does not mark its downloads as quarantined, so the steps above should just work. If you downloaded the file with a browser instead, macOS may refuse to open it ("cannot be opened because the developer cannot be verified"). Remove the quarantine flag and run it again:

```sh
xattr -d com.apple.quarantine /usr/local/bin/plan
```

(or `~/bin/plan`).

## With Go

If you have Go 1.26 or later:

```sh
go install github.com/mglass/nowfile/cmd/plan@latest
```

This puts `plan` in `$(go env GOPATH)/bin`, usually `~/go/bin`, which must be on your PATH. It works on any platform Go supports, Linux and Windows included.

## First run

Create a token on your account page at https://nowfile.org/account, under Tokens: type a label such as `laptop` and press Create token. The page shows the token once; copy it then.

```sh
plan login
```

`plan login` asks for the server (press Return for https://nowfile.org, or type your own if you self-host), the token, and optionally your username so that `plan show` with no argument shows your own page. It checks the token with the server, then saves everything to `~/.config/nowfile/config`.

Then:

```sh
plan "writing docs"
plan --until 30m "lunch"
plan --clear
plan show mike@nowfile.org
```

`plan help` lists every command.
