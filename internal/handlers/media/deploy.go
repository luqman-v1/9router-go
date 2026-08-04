package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

const (
	vercelAPI  = "https://api.vercel.com"
	denoV2API  = "https://api.deno.com/v2"
	cfAPI      = "https://api.cloudflare.com/client/v4"
	maxBodyLen = 1 << 20 // 1 MiB cap on platform API response bodies
)

// Relay function source code deployed to Vercel.
// Forwards requests to target URL specified in x-relay-target header.
const vercelRelayCode = `
export const config = { runtime: "edge" };

export default async function handler(req) {
  const target = req.headers.get("x-relay-target");
  const relayPath = req.headers.get("x-relay-path") || "/";
  if (!target) {
    return new Response(JSON.stringify({ error: "Missing x-relay-target header" }), {
      status: 400,
      headers: { "content-type": "application/json" },
    });
  }

  const targetUrl = target.replace(/\/$/, "") + relayPath;

  const headers = new Headers(req.headers);
  headers.delete("x-relay-target");
  headers.delete("x-relay-path");
  headers.delete("host");

  const response = await fetch(targetUrl, {
    method: req.method,
    headers,
    body: req.method !== "GET" && req.method !== "HEAD" ? req.body : undefined,
    duplex: "half",
  });

  return new Response(response.body, {
    status: response.status,
    headers: response.headers,
  });
}
`

// Relay worker source code deployed to Cloudflare.
const cloudflareRelayCode = `
export default {
  async fetch(request, env, ctx) {
    const target = request.headers.get("x-relay-target");
    const relayPath = request.headers.get("x-relay-path") || "/";

    if (!target) {
      return new Response(JSON.stringify({ error: "Missing x-relay-target header" }), {
        status: 400,
        headers: { "content-type": "application/json" },
      });
    }

    const targetUrl = target.replace(/\/$/, "") + relayPath;
    const newRequestInit = {
      method: request.method,
      headers: new Headers(request.headers),
    };

    if (request.method !== "GET" && request.method !== "HEAD") {
      newRequestInit.body = request.body;
      newRequestInit.duplex = "half";
    }

    newRequestInit.headers.delete("x-relay-target");
    newRequestInit.headers.delete("x-relay-path");
    newRequestInit.headers.delete("host");

    try {
      const response = await fetch(targetUrl, newRequestInit);
      return new Response(response.body, {
        status: response.status,
        headers: response.headers,
      });
    } catch (error) {
      return new Response(JSON.stringify({ error: error.message }), {
        status: 502,
        headers: { "content-type": "application/json" },
      });
    }
  },
};
`

// Relay worker source code deployed to Deno.
const denoRelayCode = `Deno.serve(async (request) => {
  const target = request.headers.get("x-relay-target");
  const relayPath = request.headers.get("x-relay-path") || "/";

  if (!target) {
    return new Response(JSON.stringify({ error: "Missing x-relay-target header" }), {
      status: 400,
      headers: { "content-type": "application/json" },
    });
  }

  const targetUrl = target.replace(/\/$/, "") + relayPath;
  const newHeaders = new Headers(request.headers);
  newHeaders.delete("x-relay-target");
  newHeaders.delete("x-relay-path");
  newHeaders.delete("host");

  const init = {
    method: request.method,
    headers: newHeaders,
  };

  if (request.method !== "GET" && request.method !== "HEAD") {
    init.body = request.body;
    init.duplex = "half";
  }

  try {
    const response = await fetch(targetUrl, init);
    return new Response(response.body, {
      status: response.status,
      headers: response.headers,
    });
  } catch (error) {
    return new Response(JSON.stringify({ error: error.message }), {
      status: 502,
      headers: { "content-type": "application/json" },
    });
  }
});`

// relayProjectName mirrors the Next routes' `body.projectName?.trim() || relay-<base36 millis>`.
func relayProjectName(name string) string {
	if n := strings.TrimSpace(name); n != "" {
		return n
	}
	return "relay-" + strconv.FormatInt(time.Now().UnixMilli(), 36)
}

// platformRequest performs an authenticated JSON API request and returns the
// status code and response body capped at maxBodyLen.
func platformRequest(ctx context.Context, client *http.Client, method, url, token string, body []byte) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return 0, nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyLen))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, raw, nil
}

// pollDeployStatus polls a platform deployment status endpoint every interval
// until check returns done=true or a non-nil error, or attempts are exhausted.
// Returns the last parsed JSON body.
func pollDeployStatus(ctx context.Context, client *http.Client, url, token string, interval time.Duration, attempts int, check func(map[string]any) (bool, error)) (map[string]any, error) {
	for i := 0; i < attempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(interval):
			}
		}
		status, raw, err := platformRequest(ctx, client, http.MethodGet, url, token, nil)
		if err != nil {
			return nil, err
		}
		data := map[string]any{}
		if status == http.StatusOK {
			json.Unmarshal(raw, &data) // best-effort parse
		}
		done, err := check(data)
		if err != nil {
			return data, err
		}
		if done {
			return data, nil
		}
	}
	return nil, fmt.Errorf("deployment timed out")
}

// POST /proxy-pools/vercel-deploy
func (h *MediaHandler) HandleVercelDeploy(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	var reqBody struct {
		VercelToken string `json:"vercelToken"`
		ProjectName string `json:"projectName"`
	}
	if err := json.Unmarshal(body, &reqBody); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	vercelToken := strings.TrimSpace(reqBody.VercelToken)
	if vercelToken == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Vercel API token is required")
		return
	}
	projectName := relayProjectName(reqBody.ProjectName)

	pkgJSON, _ := json.Marshal(map[string]any{"name": projectName, "version": "1.0.0"})
	vercelJSON, _ := json.Marshal(map[string]any{
		"rewrites": []any{map[string]any{"source": "/(.*)", "destination": "/api/relay"}},
	})
	deployBody, _ := json.Marshal(map[string]any{
		"name": projectName,
		"files": []any{
			map[string]any{"file": "api/relay.js", "data": vercelRelayCode},
			map[string]any{"file": "package.json", "data": string(pkgJSON)},
			map[string]any{"file": "vercel.json", "data": string(vercelJSON)},
		},
		"projectSettings": map[string]any{"framework": nil},
		"target":          "production",
	})

	status, raw, err := platformRequest(r.Context(), h.Client, http.MethodPost, vercelAPI+"/v13/deployments", vercelToken, deployBody)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "upstream error: "+err.Error())
		return
	}
	if status < 200 || status >= 300 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		json.Unmarshal(raw, &e)
		msg := e.Error.Message
		if msg == "" {
			msg = "Failed to create Vercel deployment"
		}
		handlerutil.WriteJSONError(w, status, msg)
		return
	}
	var deployment struct {
		ID        string `json:"id"`
		UID       string `json:"uid"`
		ProjectID string `json:"projectId"`
	}
	json.Unmarshal(raw, &deployment)
	deploymentID := deployment.ID
	if deploymentID == "" {
		deploymentID = deployment.UID
	}
	projectID := deployment.ProjectID
	if projectID == "" {
		projectID = projectName
	}

	// Disable deployment protection (Vercel Authentication) — best effort.
	if p, err := json.Marshal(map[string]any{"ssoProtection": nil}); err == nil {
		platformRequest(r.Context(), h.Client, http.MethodPatch, vercelAPI+"/v9/projects/"+projectID, vercelToken, p)
	}

	ready, err := pollDeployStatus(r.Context(), h.Client, vercelAPI+"/v13/deployments/"+deploymentID, vercelToken, 3*time.Second, 40, func(data map[string]any) (bool, error) {
		switch state := handlerutil.GetString(data, "readyState"); state {
		case "READY":
			return true, nil
		case "ERROR", "CANCELED":
			return false, fmt.Errorf("Deployment failed: %s", state)
		}
		return false, nil
	})
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	deployURL := "https://" + handlerutil.GetString(ready, "url")

	pool, err := h.Repo.InsertProxyPool(db.ProxyPoolData{
		Name: projectName, ProxyURL: deployURL, NoProxy: "", Type: "vercel", StrictProxy: false,
	})
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusCreated, map[string]any{"proxyPool": pool, "deployUrl": deployURL})
}

// POST /proxy-pools/deno-deploy
func (h *MediaHandler) HandleDenoDeploy(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	var reqBody struct {
		DenoToken   string `json:"denoToken"`
		OrgDomain   string `json:"orgDomain"`
		ProjectName string `json:"projectName"`
	}
	if err := json.Unmarshal(body, &reqBody); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	orgDomain := strings.TrimSpace(reqBody.OrgDomain)
	if orgDomain == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Organization domain is required")
		return
	}
	denoToken := strings.TrimSpace(reqBody.DenoToken)
	if denoToken == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Deno Deploy API token is required")
		return
	}
	projectName := relayProjectName(reqBody.ProjectName)

	createBody, _ := json.Marshal(map[string]any{
		"slug":   projectName,
		"labels": map[string]any{"custom.kind": "9router-relay"},
		"config": map[string]any{
			"install": "deno install",
			"runtime": map[string]any{"type": "dynamic", "entrypoint": "main.ts"},
		},
	})
	status, raw, err := platformRequest(r.Context(), h.Client, http.MethodPost, denoV2API+"/apps", denoToken, createBody)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "upstream error: "+err.Error())
		return
	}
	if status < 200 || status >= 300 {
		if status == http.StatusConflict {
			handlerutil.WriteJSONError(w, http.StatusConflict, fmt.Sprintf("App %q already exists. Choose a different name.", projectName))
			return
		}
		handlerutil.WriteJSONError(w, status, fmt.Sprintf("Failed to create app (%d): %s", status, string(raw)))
		return
	}
	var app struct {
		ID string `json:"id"`
	}
	json.Unmarshal(raw, &app)

	deployBody, _ := json.Marshal(map[string]any{
		"assets": map[string]any{
			"main.ts": map[string]any{"kind": "file", "content": denoRelayCode, "encoding": "utf-8"},
		},
	})
	status, raw, err = platformRequest(r.Context(), h.Client, http.MethodPost, denoV2API+"/apps/"+app.ID+"/deploy", denoToken, deployBody)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "upstream error: "+err.Error())
		return
	}
	if status < 200 || status >= 300 {
		deleteDenoApp(r.Context(), h.Client, denoToken, app.ID)
		handlerutil.WriteJSONError(w, status, fmt.Sprintf("Deploy failed (%d): %s", status, string(raw)))
		return
	}
	var revision struct {
		ID string `json:"id"`
	}
	json.Unmarshal(raw, &revision)

	rev, err := pollDeployStatus(r.Context(), h.Client, denoV2API+"/revisions/"+revision.ID, denoToken, 2*time.Second, 30, func(data map[string]any) (bool, error) {
		return handlerutil.GetString(data, "status") == "succeeded", nil
	})
	if err != nil {
		deleteDenoApp(r.Context(), h.Client, denoToken, app.ID)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if handlerutil.GetString(rev, "status") != "succeeded" {
		deleteDenoApp(r.Context(), h.Client, denoToken, app.ID)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, fmt.Sprintf("Deploy failed with status: %s", handlerutil.GetString(rev, "status")))
		return
	}

	orgSlug := strings.Split(orgDomain, ".")[0]
	deployURL := fmt.Sprintf("https://%s.%s.deno.net", projectName, orgSlug)

	pool, err := h.Repo.InsertProxyPool(db.ProxyPoolData{
		Name: projectName, ProxyURL: deployURL, NoProxy: "", Type: "deno", StrictProxy: false,
	})
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusCreated, map[string]any{"proxyPool": pool, "deployUrl": deployURL})
}

func deleteDenoApp(ctx context.Context, client *http.Client, token, appID string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, denoV2API+"/apps/"+appID, nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}

// POST /proxy-pools/cloudflare-deploy
func (h *MediaHandler) HandleCloudflareDeploy(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	var reqBody struct {
		AccountID   string `json:"accountId"`
		APIToken    string `json:"apiToken"`
		ProjectName string `json:"projectName"`
	}
	if err := json.Unmarshal(body, &reqBody); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	accountID := strings.TrimSpace(reqBody.AccountID)
	apiToken := strings.TrimSpace(reqBody.APIToken)
	if accountID == "" || apiToken == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Cloudflare Account ID and API Token are required")
		return
	}
	projectName := relayProjectName(reqBody.ProjectName)
	workerScriptURL := fmt.Sprintf("%s/accounts/%s/workers/scripts/%s", cfAPI, accountID, projectName)

	// Upload Worker Script (Cloudflare requires multipart/form-data).
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	indexHdr := make(textproto.MIMEHeader)
	indexHdr.Set("Content-Disposition", `form-data; name="index.js"; filename="index.js"`)
	indexHdr.Set("Content-Type", "application/javascript+module")
	indexPart, _ := mw.CreatePart(indexHdr)
	indexPart.Write([]byte(cloudflareRelayCode))

	metaJSON, _ := json.Marshal(map[string]any{
		"main_module":        "index.js",
		"compatibility_date": "2024-03-20",
		"observability":      map[string]any{"enabled": true},
	})
	metaHdr := make(textproto.MIMEHeader)
	metaHdr.Set("Content-Disposition", `form-data; name="metadata"; filename="metadata.json"`)
	metaHdr.Set("Content-Type", "application/json")
	metaPart, _ := mw.CreatePart(metaHdr)
	metaPart.Write(metaJSON)
	mw.Close()

	uploadReq, err := http.NewRequestWithContext(r.Context(), http.MethodPut, workerScriptURL, &buf)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	uploadReq.Header.Set("Authorization", "Bearer "+apiToken)
	uploadReq.Header.Set("Content-Type", mw.FormDataContentType())
	uploadRes, err := h.Client.Do(uploadReq)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "upstream error: "+err.Error())
		return
	}
	raw, _ := io.ReadAll(io.LimitReader(uploadRes.Body, maxBodyLen))
	uploadRes.Body.Close()
	if uploadRes.StatusCode < 200 || uploadRes.StatusCode >= 300 {
		var e struct {
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		json.Unmarshal(raw, &e)
		msg := "Failed to upload Worker to Cloudflare"
		if len(e.Errors) > 0 && e.Errors[0].Message != "" {
			msg = e.Errors[0].Message
		}
		handlerutil.WriteJSONError(w, uploadRes.StatusCode, msg)
		return
	}

	// Enable workers.dev subdomain for the script — best effort.
	if sub, err := json.Marshal(map[string]any{"enabled": true}); err == nil {
		platformRequest(r.Context(), h.Client, http.MethodPost, workerScriptURL+"/subdomain", apiToken, sub)
	}

	// Get the workers.dev subdomain for the account to construct the final URL.
	deployURL := ""
	status, raw, err := platformRequest(r.Context(), h.Client, http.MethodGet, fmt.Sprintf("%s/accounts/%s/workers/subdomain", cfAPI, accountID), apiToken, nil)
	if err == nil && status == http.StatusOK {
		var sub struct {
			Result struct {
				Subdomain string `json:"subdomain"`
			} `json:"result"`
		}
		if json.Unmarshal(raw, &sub) == nil && sub.Result.Subdomain != "" {
			deployURL = fmt.Sprintf("https://%s.%s.workers.dev", projectName, sub.Result.Subdomain)
		}
	}
	if deployURL == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest,
			"Worker deployed but failed to retrieve workers.dev subdomain. Make sure you have setup a workers.dev subdomain in Cloudflare Dashboard.")
		return
	}

	pool, err := h.Repo.InsertProxyPool(db.ProxyPoolData{
		Name: projectName, ProxyURL: deployURL, NoProxy: "", Type: "cloudflare", StrictProxy: false,
	})
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusCreated, map[string]any{"proxyPool": pool, "deployUrl": deployURL})
}
