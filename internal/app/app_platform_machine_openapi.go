package app

import (
	"encoding/json"
	"net/http"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// The generated OpenAPI document (doc 183 section 4.5): one GET operation per route the token may
// call, the declared query names as string parameters, and the bearer security scheme, so Dify's
// custom-tool import can read it. It is built from the descriptor and the token's effective routes
// alone and costs no backend call.

func (s *Server) machineBaseURL(r *http.Request) string {
	if base := s.publicBaseURL(); base != "" {
		return base
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func platformMachineOpenAPI(base string, installation string, a platformMachineAuthority, routes []string) map[string]any {
	errorSchema := map[string]any{"type": "object", "properties": map[string]any{"code": map[string]any{"type": "string"}}}
	failure := func(text string) map[string]any {
		return map[string]any{"description": text, "content": map[string]any{"application/json": map[string]any{"schema": errorSchema}}}
	}
	paths := map[string]any{}
	for _, id := range routes {
		route, ok := a.route(id)
		if !ok {
			continue
		}
		params := []any{}
		for _, name := range route.Query {
			params = append(params, map[string]any{"name": name, "in": "query", "required": false, "schema": map[string]any{"type": "string", "maxLength": appplatform.MaxMachineQueryValue}})
		}
		operation := map[string]any{
			"operationId": route.ID,
			"summary":     route.ID,
			"parameters":  params,
			"security":    []any{map[string]any{"bearerAuth": []any{}}},
			"responses": map[string]any{
				"200": map[string]any{"description": "The app's JSON answer.", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object", "additionalProperties": true}}}},
				"400": failure("The query is not one this route declares."),
				"401": failure("The token is missing, wrong, expired, revoked or not valid from this address."),
				"403": failure("The token does not list this route."),
				"429": failure("A rate limit was reached; see Retry-After."),
				"502": failure("The app answered outside its contract."),
				"503": failure("The app is not available right now."),
			},
		}
		if route.Description != "" {
			operation["description"] = route.Description
			operation["summary"] = route.Description
		}
		paths["/machine/apps/"+installation+"/data/"+route.ID] = map[string]any{"get": operation}
	}
	return map[string]any{
		"openapi": "3.0.3",
		"info":    map[string]any{"title": a.descriptor.appID + " machine read API", "version": a.descriptor.version},
		"servers": []any{map[string]any{"url": base}},
		"paths":   paths,
		"components": map[string]any{"securitySchemes": map[string]any{
			"bearerAuth": map[string]any{"type": "http", "scheme": "bearer", "description": "A machine token issued by the Portal administrator (pmr_...)."},
		}},
		"security": []any{map[string]any{"bearerAuth": []any{}}},
	}
}

func (s *Server) machineOpenAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		machineMethodNotAllowed(w)
		return
	}
	client, ok := s.machineAuthenticate(w, r)
	if !ok {
		return
	}
	auth, ok := s.machineAuthority(w, r, client)
	if !ok {
		return
	}
	routes := client.token.effectiveRoutes(auth.policy)
	doc, err := json.Marshal(platformMachineOpenAPI(s.machineBaseURL(r), auth.installation, auth, routes))
	if err != nil {
		platformMachineFail(w, 503, "unavailable", 0)
		return
	}
	platformMachineHeaders(w)
	w.WriteHeader(200)
	w.Write(doc)
	s.recordPlatformMachine(client, "bytes", len(doc))
}
