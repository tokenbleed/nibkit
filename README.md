# nibkit

NIBArchive decompiler for iOS/macOS pentests. Parses compiled `.nib` /
`.storyboardc` files (the format real apps ship), extracts object structure,
wiring, custom classes, and the navigation graph. Single static Go binary,
stdlib only, cross-compiles anywhere.

`strings` gives you tokens. nibkit gives you **relationships**: which control
fires which selector, which custom class backs which scene, how navigation
flows, and what runtime attributes were set. That is the attack surface
`strings` cannot show.

## usage

    nibkit [command] <path...> [flags]
    nibkit                       # no args + terminal: interactive menu

    commands (default = object tree with header):
      wiring     outlets + @IBAction selectors + runtime attributes
      classes    custom (UIClassSwapper) IB classes
      segues     navigation graph: segue templates + container children
      all        classes + wiring + navigation in one report
      info       header counts only

    input: .ipa (auto-extracted), .nib, .storyboardc, .app, or any
    directory (walked recursively for NIBArchive nibs). Multiple paths
    are aggregated.

    flags:
      -J, --json     JSON (single object for one nib, array for many)
          --frida    generate Frida hook stubs from @IBAction wiring
          --mermaid  emit navigation graph as a Mermaid flowchart

## examples

    nibkit Foo.ipa                       # .ipa: auto-extracted, no unzip step
    nibkit wiring Foo.app                # outlets + actions + runtime attrs
    nibkit wiring --frida Foo.app        # hooks.js: Interceptor.attach stubs
    nibkit segues Foo.app                # navigation graph, container-aware
    nibkit segues --mermaid Foo.app      # flowchart (renders on GitHub)
    nibkit all Foo.app                   # full report in one run
    nibkit -J segues Foo.app | jq '.[] | .navigation'

Tables size to the terminal width and long reports page through `$PAGER`
(set `NIBKIT_PAGER=cat` to disable). Piped output stays raw for `grep`/`jq`.
For raw strings just run `strings -a` on the nib files.

## what each command gives you

- **tree** (default): the object graph, indented, with resolved custom-class
  annotations (`UIClassSwapper` shows the real class).
- **wiring**: the high-value command. Tables outlets (`myButton ->
  MyViewController`), actions (`didTapLogin: [touchUpInside]`), and a
  RUNTIME ATTRS section (`keyPath=value`, a classic place to hide flags and
  feature switches). Sources/destinations resolve through proxies and class
  swappers to real names.
- **classes**: every custom IB class with its base class and scene ID,
  Swift-mangled names recovered.
- **segues**: the full navigation graph. Storyboard segue templates (kind,
  identifier, selector, custom segue class) plus container relationships
  (tab-bar tabs, navigation roots) that never compile to segues. Deduped.
- **--frida**: an `Interceptor.attach` stub per `@IBAction`, with the
  implementing class resolved where possible.
- **--mermaid**: the same graph as a Mermaid `flowchart LR`, useful in
  findings reports and GitHub markdown.

## build

    go build -trimpath -o nibkit .
    # cross-compile: GOOS/GOARCH (darwin/linux, arm64/amd64)

## format notes

Compiled nibs use the NIBArchive container (magic `NIBArchive`), not
NSKeyedArchive. Header is 50 bytes; keys, class names, objects, then values.
Varints are 7 bits/byte little-endian with the high bit SET on the terminal
byte (opposite of protobuf). Works for coderVersion 9 and 10+ with no version
gating.
