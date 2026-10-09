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
    <link rel="icon" href="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 32 32'%3E%3Crect width='32' height='32' rx='7' fill='%23e56a4a'/%3E%3Ctext x='16' y='23' font-family='system-ui,sans-serif' font-size='19' font-weight='700' fill='%23fff' text-anchor='middle'%3E9%3C/text%3E%3C/svg%3E" />
  </head>
  <body>
    <a class="skip-link" href="#readme">Skip to content</a>

    <header class="masthead">
      <div class="masthead-inner">
        <a class="wordmark" href="index.html">
          <span class="wordmark-mark" aria-hidden="true">9</span>
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
