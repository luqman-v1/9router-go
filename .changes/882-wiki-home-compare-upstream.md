### Wiki: the home page compares with upstream 9Router

The wiki's home page listed "Compare with upstream 9Router" as one row among nineteen in the "Start here" table, and the comparison itself lived only on the Parity page. A reader deciding whether to switch from the Node.js version had to navigate away to find out whether the API is the same — which is the one thing they were choosing between.

- **A "How it compares with 9Router" section** now sits on the home page, between the entry-point table and "Running in 30 seconds": the six-row table from Parity (runtime, dashboard, database, schema, Docker, port), then what is identical, what 9router-go adds, and what differs by design, with a link to Parity for the rest.
- **Facts come from `wiki/parity.md`**, not from memory. The table is the same table, so the two pages cannot drift.
- **Every number is the shipped one.** `20130` and `20128`, Svelte 5 and React 19, and the `~/.9router/db/data.sqlite` path the project already documents.

Verified in a browser: the heading is `How it compares with 9Router`, the table renders all 6 rows with 3 columns, and at 390px it fits inside its own scroll wrapper (`overflow-x: auto`, 318px) without widening the document.
