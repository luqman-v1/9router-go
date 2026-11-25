package main

// pageTemplate is the whole chrome around the rendered README: a masthead with
// the three links a visitor to a project page actually reaches for, the article
// itself, and a footer pointing back at the README on GitHub.
//
// Paths are relative because the site is served from the /9router-go/ project
// subpath, where a root-absolute /style.css would 404.
const pageTemplate = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>9router-go</title>
    <meta
      name="description"
      content="9router-go is a single-binary AI gateway and token saver in Go. Serve OpenAI, Claude and Gemini compatible APIs to your CLI tools with automatic fallback across 40+ providers."
    />
    <link rel="stylesheet" href="style.css" />
    <!-- The product's own icon, the same SVG the dashboard serves from
         web/public/favicon.svg. Inlined as a data URI rather than shipped as a
         second asset for a 48px image. -->
    <link rel="icon" href="` + brandMarkPNG + `" />
  </head>
  <body>
    <a class="skip-link" href="#readme">Skip to content</a>

    <header class="masthead">
      <div class="masthead-inner">
        <a class="wordmark" href="index.html">
          <!-- The product's own icon: the same mark the dashboard and the
               repository use, the shared brandMarkPNG embedded in an SVG so it
               is the real glyph rather than a letter in a coloured box.
               aria-hidden, because the word beside it is the accessible name. -->
          <span class="wordmark-mark" aria-hidden="true">
            <svg viewBox="0 0 512 512" focusable="false">
              <image width="512" height="512" href="` + brandMarkPNG + `"/>
            </svg>
          </span>
          <span>9router-go</span>
        </a>
        <nav aria-label="Project">
          <a href="https://github.com/{{ .Repo }}">Repository</a>
          <a href="https://github.com/{{ .Repo }}/releases">Releases</a>
          <a href="tg">Telegram group</a>
        </nav>
      </div>
    </header>

    <!-- Rendered from README.md by tools/sitegen, so the two can never disagree.
         The HTML passes through unescaped because it is this repository's own
         README: the same file GitHub renders for everyone reading the repo. -->
    <main id="readme" class="prose">{{ .Body }}</main>

    <footer class="colophon">
      <p>
        This page is the
        <a href="{{ blob "README.md" }}">README</a>, generated on every push to
        <code>main</code>. Content licensed
        <a href="{{ blob "LICENSE" }}">MIT</a>.
      </p>
    </footer>
  </body>
</html>
`
