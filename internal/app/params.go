package app

import (
	"os"

	"github.com/urfave/cli/v2"
)

// CLIParams holds command-line flags and environment overrides.
type CLIParams struct {
	RTK              bool
	RTKSet           bool
	Caveman          bool
	CavemanSet       bool
	Ponytail         bool
	PonytailSet      bool
	ADHD             bool
	ADHDSet          bool
	AutoUpdate       bool
	NoInjectionGuard bool
}

// NewCLIParams creates CLIParams from a urfave/cli Context.
func NewCLIParams(cCtx *cli.Context) CLIParams {
	if cCtx == nil {
		return DefaultCLIParams()
	}
	return CLIParams{
		RTK:              cCtx.Bool("rtk"),
		RTKSet:           cCtx.IsSet("rtk"),
		Caveman:          cCtx.Bool("caveman"),
		CavemanSet:       cCtx.IsSet("caveman"),
		Ponytail:         cCtx.Bool("ponytail"),
		PonytailSet:      cCtx.IsSet("ponytail"),
		ADHD:             cCtx.Bool("adhd"),
		ADHDSet:          cCtx.IsSet("adhd"),
		AutoUpdate:       cCtx.Bool("auto-update"),
		NoInjectionGuard: cCtx.Bool("no-injection-guard"),
	}
}

// DefaultCLIParams creates CLIParams using default environment variables.
func DefaultCLIParams() CLIParams {
	return CLIParams{
		RTK:              os.Getenv("RTK_ENABLED") != "false",
		RTKSet:           os.Getenv("RTK_ENABLED") != "",
		Caveman:          os.Getenv("CAVEMAN_ENABLED") == "true",
		CavemanSet:       os.Getenv("CAVEMAN_ENABLED") != "",
		Ponytail:         os.Getenv("PONYTAIL_ENABLED") == "true",
		PonytailSet:      os.Getenv("PONYTAIL_ENABLED") != "",
		ADHD:             os.Getenv("ADHD_ENABLED") == "true",
		ADHDSet:          os.Getenv("ADHD_ENABLED") != "",
		AutoUpdate:       os.Getenv("AUTO_UPDATE") == "true",
		NoInjectionGuard: os.Getenv("INJECTION_GUARD_DISABLED") == "true",
	}
}
