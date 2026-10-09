package executor

import "strings"

// Qoder regional endpoints — port of open-sse/shared/qoder/constants.js
// (QODER_REGION_BASES, qoderRegionOf, qoderInferenceBase, qoderJobTokenExchangeUrl,
// qoderUserInfoUrl, qoderModelListUrl, QODER_CHAT_SIG_PATH).
//
// Qoder and Qoder CN are separate deployments with separate credentials, so
// each keeps its own set and neither is ever routed to the other
// (AGENTS.md section 3.A). These live in the executor package rather than the
// dashboard because chat needs them too: model_config comes from the same
// COSY-signed model list the dashboard reads.

// QoderRegion is the deployment a request belongs to.
type QoderRegion string

const (
	QoderRegionIntl QoderRegion = "intl"
	QoderRegionCN   QoderRegion = "cn"
)

// qoderJobTokenPrefix marks a short-lived job token. api3 rejects jt- traffic
// with "Login expired" (403); the official qodercli serves it from api2.
// Device tokens (dt-) stay on api3.
const qoderJobTokenPrefix = "jt-"

// QoderChatSigPath is the COSY-signed inference path, relative to /algo.
const QoderChatSigPath = "/api/v2/service/pro/sse/agent_chat_generation"

// QoderEndpoints holds the hosts one Qoder deployment serves.
type QoderEndpoints struct {
	JobTokenExchangeURL string
	UserInfoURL         string
	ModelListURL        string
	// ModelListURLAlt serves job-token traffic. Empty for CN, which has no
	// api2-style split — its single gateway serves every token kind.
	ModelListURLAlt string
}

// QoderRegionOf maps a provider id to its deployment. Anything that is not
// qoder-cn is intl.
func QoderRegionOf(providerID string) QoderRegion {
	if providerID == "qoder-cn" {
		return QoderRegionCN
	}
	return QoderRegionIntl
}

// QoderEndpointsFor returns the endpoint set for one provider id.
func QoderEndpointsFor(providerID string) QoderEndpoints {
	if QoderRegionOf(providerID) == QoderRegionCN {
		return QoderEndpoints{
			JobTokenExchangeURL: "https://openapi.qoder.com.cn/api/v1/jobToken/exchange",
			UserInfoURL:         "https://openapi.qoder.com.cn/api/v1/userinfo",
			ModelListURL:        "https://gateway.qoder.com.cn/algo/api/v2/model/list",
		}
	}
	return QoderEndpoints{
		JobTokenExchangeURL: "https://openapi.qoder.sh/api/v1/jobToken/exchange",
		UserInfoURL:         "https://openapi.qoder.sh/api/v1/userinfo",
		ModelListURL:        "https://api3.qoder.sh/algo/api/v2/model/list",
		ModelListURLAlt:     "https://api2.qoder.sh/algo/api/v2/model/list",
	}
}

// ModelListURLForToken picks the host that serves this credential's token kind.
// A job token needs the alt host; everything else uses the primary.
func (e QoderEndpoints) ModelListURLForToken(token string) string {
	if strings.HasPrefix(token, qoderJobTokenPrefix) && e.ModelListURLAlt != "" {
		return e.ModelListURLAlt
	}
	return e.ModelListURL
}
