# Publishing Sweep

## How distribution works

A **Git tag** such as `v0.3.1` names an exact source commit. A **GitHub release** adds a public download page, notes, and compiled archives to that tag. The **Homebrew formula** tells brew which archive matches the user's Mac and verifies it against a SHA-256 checksum.

This repository also acts as a custom Homebrew tap. It is separate from Homebrew's main catalog, so no Homebrew/core approval is required. Users add the tap with its explicit Git URL because this repository is named `sweep`, not `homebrew-sweep`.

## Publish a new version

Use a Mac with Go, Git, and an authenticated GitHub CLI (`gh auth login`). Start from a clean checkout of `main` with the changes you want to ship.

1. Update `version` in `main.go`. Use a patch bump for fixes, a minor bump for features, and a major bump for breaking changes once the project reaches 1.0.
2. Write notes in `docs/releases/vX.Y.Z.md`.
3. Run checks and package the release:

   ```sh
   go test -race ./...
   go vet ./...
   ./scripts/package-release.sh
   ```

   This creates Apple Silicon and Intel archives plus `dist/checksums.txt`, and updates `Formula/sweep.rb` with the version, download URLs, and exact checksums. It does not publish anything. Archives are excluded from Git. The build disables embedded VCS state because the formula is generated from the archive hashes before the release commit is created.

4. Test the native archive's executable and review the formula and release notes. Do not change the packaged source, README, or license after this step without rebuilding.
5. Commit the changes and tag that commit. Replace `0.3.2` below with the version you just packaged:

   ```sh
   git add main.go README.md Formula/sweep.rb docs scripts .gitignore
   git commit -m "Release v0.3.2"
   git tag -a v0.3.2 -m "Sweep v0.3.2"
   git push origin main v0.3.2
   gh release create v0.3.2 \
     dist/sweep_0.3.2_darwin_arm64.tar.gz \
     dist/sweep_0.3.2_darwin_amd64.tar.gz \
     dist/checksums.txt \
     --verify-tag --title 'Sweep v0.3.2' \
     --notes-file docs/releases/v0.3.2.md
   ```

6. Verify the release downloads and formula:

   ```sh
   brew update
   brew install jacobslunga/sweep/sweep  # or brew upgrade if already installed
   brew test jacobslunga/sweep/sweep
   ```

Always publish the exact archives whose checksums are committed in the formula. Do not replace a published version's assets or move its tag; publish a new version instead. No CI publishing secrets are needed for this local release process.

## Distribution limits

Sweep currently targets macOS. Apple Silicon can be tested locally; an Intel build needs an Intel Mac for runtime validation. The archives are not Apple Developer ID signed or notarized. Homebrew checks their integrity using the published SHA-256 hashes. Direct browser downloads may be subject to macOS Gatekeeper checks.
