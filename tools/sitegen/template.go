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
    <link rel="icon" href="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' fill='none' viewBox='0 0 48 48'%3E%3Crect width='48' height='48' fill='%23181D27' rx='12'/%3E%3Crect width='47' height='47' x='.5' y='.5' stroke='%232B3245' rx='11.5'/%3E%3Ccircle cx='24' cy='24' r='7' fill='%23FF5C35'/%3E%3Ccircle cx='14' cy='16' r='3.5' fill='%23FF8469'/%3E%3Ccircle cx='34' cy='16' r='3.5' fill='%2338BDF8'/%3E%3Ccircle cx='14' cy='32' r='3.5' fill='%2334D399'/%3E%3Ccircle cx='34' cy='32' r='3.5' fill='%23A78BFA'/%3E%3Cpath stroke='%2394A3B8' stroke-linecap='round' stroke-width='1.75' d='M16.5 18L21 21.5M31.5 18L27 21.5M16.5 30L21 26.5M31.5 30L27 26.5'/%3E%3C/svg%3E" />
  </head>
  <body>
    <a class="skip-link" href="#readme">Skip to content</a>

    <header class="masthead">
      <div class="masthead-inner">
        <a class="wordmark" href="index.html">
          <!-- The product's own icon: the same mark the dashboard and the
               repository use, drawn as inline SVG so it is the real glyph at any
               size and not a letter in a coloured box. aria-hidden, because the
               word beside it is the accessible name. -->
          <span class="wordmark-mark" aria-hidden="true">
            <svg viewBox="0 0 48 48" focusable="false">
              <rect width="48" height="48" fill="#181D27" rx="12"/>
              <rect width="47" height="47" x=".5" y=".5" stroke="#2B3245" rx="11.5"/>
              <circle cx="24" cy="24" r="7" fill="#FF5C35"/>
              <circle cx="14" cy="16" r="3.5" fill="#FF8469"/>
              <circle cx="34" cy="16" r="3.5" fill="#38BDF8"/>
              <circle cx="14" cy="32" r="3.5" fill="#34D399"/>
              <circle cx="34" cy="32" r="3.5" fill="#A78BFA"/>
              <path stroke="#94A3B8" stroke-linecap="round" stroke-width="1.75" d="M16.5 18L21 21.5M31.5 18L27 21.5M16.5 30L21 26.5M31.5 30L27 26.5"/>
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
