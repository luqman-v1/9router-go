package observ

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Handler serves the default instance's private registry in the Prometheus
// text exposition format.
//
// It is deliberately not mounted here: /metrics is an operator endpoint and
// the router mounts it inside the RequireDashboardAuth group, alongside the
// rest of the dashboard API.
func Handler() http.Handler {
	return HandlerFor(Default())
}

// HandlerFor serves a specific instance's registry. Useful for a second
// gateway embedded in the same process, and for tests.
func HandlerFor(m *Metrics) http.Handler {
	return promhttp.HandlerFor(m.Registry(), promhttp.HandlerOpts{
		// Cardinality is the failure mode of this package: a mislabelled
		// series should show up as a scrape-time error rather than as
		// millions of time series nobody can delete.
		ErrorHandling: promhttp.HTTPErrorOnError,
	})
}