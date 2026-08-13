# nibkit

A modern, pentest-focused decompiler for compiled iOS/macOS Interface Builder
archives (`.nib`, `.storyboardc`, `.app`). Parses the `NIBArchive` binary format
(coderVersion 9 and 10, every Xcode since Xcode 13) and recovers object
structure, wiring, custom classes, segues, and string values. Single static Go
binary, zero runtime dependencies.

## why

`strings` gives you tokens. nibkit gives you **relationships**: which control
fires which selector, which view controller owns an outlet, which class a
storyboard scene is bound to, and what runtime attributes a developer hid on a
control. That is the attack surface `strings` cannot show.

## install

    go install nibkit@latest      # or: go build -o nibkit .

Symlink the binary onto your PATH (`~/.local/bin/nibkit`).

## usage

    nibkit [command] <path...> [flags]

    commands (default = object tree with header):
      wiring      outlets, @IBAction selectors, runtime attributes
      strings     all string values, one per line (source<TAB>value)
      classes     custom (UIClassSwapper) classes with base + scene id
      segues      storyboard segue / navigation graph
      info        header counts only

    input:
      a .nib file, a .nib/.storyboardc/.app bundle, or any directory
      (recursively walked for NIBArchive .nib files). Multiple paths aggregate.

    flags:
      -J, --json    JSON output (single object for one blob, array for many)
          --frida   generate Frida @IBAction hook stubs (wiring only)
      -V, --version print version
      -h, --help    show help

    aliases (older CLI, still work): dump/tree, info, json

## examples

    nibkit Foo.nib                          # tree + header
    nibkit wiring Foo.storyboardc           # outlets, actions, runtime attrs
    nibkit wiring --frida Foo.storyboardc   # ready-to-run Frida hooks
    nibkit strings Foo.app | grep -i http   # app-wide string sweep
    nibkit classes Foo.app                  # every custom IB class
    nibkit segues Foo.app                   # navigation graph
    nibkit -J wiring Foo.app | jq '.[] | .actions'

## what each command gives you

- **tree (default)**: full object graph with decoded geometry, bools, and custom
  class annotations (`UIClassSwapper ... <MyViewController>`).
- **wiring**: the high-value command. Tables outlets (`myButton ->
  MyViewController`), `@IBAction` selectors with decoded control events
  (`didTapButton:withEvent: [touchUpInside]`), and developer-set runtime
  attributes (`secretTag = admin-flag`) where keys, flags, and hidden URLs live.
- **classes**: every `UIClassSwapper` resolved to its real class, base class,
  and storyboard scene id. Surfaces Swift-mangled names directly.
- **segues**: every `UIStoryboardSegueTemplate` with kind (embed/show/push/...),
  source view controller, destination scene id, and prepare/perform selector.
- **strings**: every string value (class names, key paths, object bytes) for
  grepping.

## frida codegen

`nibkit wiring --frida` emits a Frida script with one `Interceptor.attach` stub
per `@IBAction`, targeting the implementing class. Storyboard placeholder
destinations are auto-resolved to the scene's view controller when the class is
unique; ambiguous cases are emitted as commented TODOs with candidate classes
listed at the top.

    frida -U -f com.example.app -l hooks.js

## format notes

NIBArchive binary layout: 50-byte header (`NIBArchive` magic + formatVersion 1 +
coderVersion 9/10 + four count/offset pairs for objects, keys, values, class
names). Integers are 7-bit little-endian varints with the high bit set on the
terminal byte. Coder value types: int8/16/32/64, true, false, float, double,
data, nil, object-ref. Geometry DATA payloads are a 1-byte tag followed by N
little-endian doubles.

Decompilation is inherently lossy: Interface Builder discards authoring metadata
at compile time, so nibkit recovers layout, structure, wiring, and string values,
not a round-trippable `.xib`.

## license

MIT.
