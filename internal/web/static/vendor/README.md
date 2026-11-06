# Vendored frontend dependencies

These files are bundled into the Hash binary with `go:embed`.

| Package | Version | Files | License |
| --- | --- | --- | --- |
| [`@xterm/xterm`](https://www.npmjs.com/package/@xterm/xterm) | 6.0.0 | `xterm.js`, `xterm.css` | MIT (`LICENSE.xterm`) |
| [`@xterm/addon-fit`](https://www.npmjs.com/package/@xterm/addon-fit) | 0.11.0 | `addon-fit.js` | MIT (`LICENSE.addon-fit`) |

Both files come unmodified from the npm tarballs on `registry.npmjs.org`:

```bash
npm pack @xterm/xterm@6.0.0
npm pack @xterm/addon-fit@0.11.0
```

Update both packages together so the terminal API and fit addon stay compatible.