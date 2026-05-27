package api

// redocHTML is the Redoc single-page renderer for /openapi.yaml.
//
// Redoc is a Redocly project that renders an OpenAPI spec as a static
// HTML page. It loads from a CDN at runtime so the dc-api binary doesn't
// need to embed the JS — only this 50-line shell. The page calls back
// to the same origin to fetch the spec, so it works anywhere dc-api is
// reachable without extra configuration.
//
// Version pin: redoc.standalone@2.x. Bump in tandem with periodic spec
// reviews; the API surface that Redoc consumes is stable across 2.x.
const redocHTML = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <title>DC-API reference</title>
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <link rel="icon" href="data:," />
    <style>body { margin: 0; padding: 0; }</style>
  </head>
  <body>
    <redoc spec-url="openapi.yaml" hide-download-button></redoc>
    <script src="https://cdn.redocly.com/redoc/v2.5.1/bundles/redoc.standalone.js"></script>
  </body>
</html>
`
