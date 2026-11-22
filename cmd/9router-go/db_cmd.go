package main

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v2"

	"9router/proxy/internal/dbmigrate"
)

// dbCommand is the `9router-go db` parent: SQLite → PostgreSQL migration
// tooling. Export reads an existing SQLite install into portable JSONL files;
// Import replays those files into a running PostgreSQL database.
func dbCommand() *cli.Command {
	return &cli.Command{
		Name:  "db",
		Usage: "Migrate the gateway database between engines (SQLite → PostgreSQL)",
		Subcommands: []*cli.Command{
			{
				Name:  "export",
				Usage: "Export every table of a SQLite database to <out>/<table>.jsonl",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "from", Usage: "path to the SQLite file", Required: true},
					&cli.StringFlag{Name: "out", Usage: "output directory for the JSONL dump", Required: true},
				},
				Action: dbExportAction,
			},
			{
				Name:  "import",
				Usage: "Import a JSONL dump into a PostgreSQL database",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "to", Usage: "PostgreSQL DSN (postgres://...)", Required: true},
					&cli.StringFlag{Name: "from", Usage: "directory holding the exported *.jsonl files", Required: true},
				},
				Action: dbImportAction,
			},
		},
	}
}

func dbExportAction(cCtx *cli.Context) error {
	from := cCtx.String("from")
	out := cCtx.String("out")
	summaries, err := dbmigrate.Export(cCtx.Context, from, out)
	if err != nil {
		return err
	}
	for _, s := range summaries {
		fmt.Println(s)
	}
	fmt.Printf("Exported %d tables to %s\n", len(summaries), out)
	return nil
}

func dbImportAction(cCtx *cli.Context) error {
	to := cCtx.String("to")
	from := cCtx.String("from")
	summaries, err := dbmigrate.Import(context.Background(), to, from)
	if err != nil {
		return err
	}
	for _, s := range summaries {
		fmt.Println(s)
	}
	fmt.Printf("Imported %d tables into PostgreSQL\n", len(summaries))
	return nil
}
