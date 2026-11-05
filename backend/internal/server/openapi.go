package server

import (
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
)

type openAPISpec map[string]any

func (a *App) registerOpenAPI() {
	if a.cfg.OpenAPIURL != "" {
		a.r.GET(a.cfg.OpenAPIURL, func(c *gin.Context) {
			c.JSON(http.StatusOK, a.openAPISpec())
		})
	}
	if a.cfg.DocsURL != "" && a.cfg.Debug {
		a.r.GET(a.cfg.DocsURL, func(c *gin.Context) {
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(swaggerUIHTML(a.cfg.OpenAPIURL)))
		})
	}
	if a.cfg.RedocURL != "" && a.cfg.Debug {
		a.r.GET(a.cfg.RedocURL, func(c *gin.Context) {
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(redocHTML(a.cfg.OpenAPIURL)))
		})
	}
}

func (a *App) openAPISpec() openAPISpec {
	paths := map[string]any{}
	routes := a.r.Routes()
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Path == routes[j].Path {
			return routes[i].Method < routes[j].Method
		}
		return routes[i].Path < routes[j].Path
	})

	for _, route := range routes {
		if route.Method == http.MethodHead || route.Method == http.MethodOptions {
			continue
		}
		if route.Path == a.cfg.OpenAPIURL || route.Path == a.cfg.DocsURL || route.Path == a.cfg.RedocURL {
			continue
		}
		path := ginPathToOpenAPI(route.Path)
		method := strings.ToLower(route.Method)
		item, _ := paths[path].(map[string]any)
		if item == nil {
			item = map[string]any{}
			paths[path] = item
		}
		item[method] = a.operationFor(route.Method, path)
	}

	return openAPISpec{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title":       "NarratoAI Backend API",
			"description": "智能视频剪辑平台 Go 后端 API",
			"version":     "0.1.0",
		},
		"servers": []map[string]any{{"url": "/"}},
		"paths":   paths,
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"BearerAuth": map[string]any{"type": "http", "scheme": "bearer", "bearerFormat": "JWT"},
			},
			"schemas": openAPISchemas(),
		},
	}
}

func (a *App) operationFor(method, path string) map[string]any {
	if operation := workflowOperationFor(method, path); operation != nil {
		return operation
	}
	operation := map[string]any{
		"summary":     summaryFor(method, path),
		"operationId": operationID(method, path),
		"tags":        []string{tagFor(path)},
		"responses": map[string]any{
			"200": map[string]any{"description": "OK", "content": jsonContent(refSchema("GenericResponse"))},
			"400": map[string]any{"description": "Bad Request", "content": jsonContent(refSchema("ErrorResponse"))},
			"401": map[string]any{"description": "Unauthorized", "content": jsonContent(refSchema("ErrorResponse"))},
			"500": map[string]any{"description": "Internal Server Error", "content": jsonContent(refSchema("ErrorResponse"))},
		},
	}
	if method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch {
		operation["requestBody"] = map[string]any{
			"required": false,
			"content":  jsonContent(map[string]any{"type": "object", "additionalProperties": true}),
		}
	}
	if shouldSecure(path) {
		operation["security"] = []map[string]any{{"BearerAuth": []string{}}}
	}
	params := pathParams(path)
	if len(params) > 0 {
		operation["parameters"] = params
	}
	if isEventStreamResponsePath(path) {
		operation["responses"] = map[string]any{
			"200": map[string]any{"description": "Server-Sent Events stream", "content": map[string]any{"text/event-stream": map[string]any{"schema": map[string]any{"type": "string"}}}},
		}
	}
	if path == "/api/v1/tools/elevenlabs/scribe/transcribe" && method == http.MethodPost {
		operation["summary"] = "Upload audio to ElevenLabs Scribe"
		operation["requestBody"] = map[string]any{
			"required": true,
			"content": map[string]any{
				"multipart/form-data": map[string]any{
					"schema": map[string]any{
						"type":     "object",
						"required": []string{"file"},
						"properties": map[string]any{
							"file":          map[string]any{"type": "string", "format": "binary"},
							"language_code": map[string]any{"type": "string", "description": "Optional ISO-639 language code"},
						},
					},
				},
			},
		}
	}
	if path == "/api/v1/tools/timeline-view/analyze" && method == http.MethodPost {
		operation["summary"] = "Analyze a timeline visual aid image with Qwen Vision"
		operation["requestBody"] = map[string]any{
			"required": true,
			"content": map[string]any{
				"application/json": map[string]any{
					"schema": map[string]any{
						"type":     "object",
						"required": []string{"image_oss_url"},
						"properties": map[string]any{
							"image_oss_url": map[string]any{"type": "string", "description": "DashScope temporary oss:// image URL"},
							"start":         map[string]any{"type": "number", "description": "Optional source range start, seconds"},
							"end":           map[string]any{"type": "number", "description": "Optional source range end, seconds"},
							"n_frames":      map[string]any{"type": "integer", "minimum": 1, "maximum": 24},
							"prompt":        map[string]any{"type": "string"},
							"model":         map[string]any{"type": "string"},
						},
					},
				},
			},
		}
	}
	if path == "/api/v1/tools/timeline-view/decide" && method == http.MethodPost {
		operation["summary"] = "Decide which ranges need timeline_view visual aids with the OpenAI flash model profile"
		operation["requestBody"] = map[string]any{
			"required": true,
			"content": map[string]any{
				"application/json": map[string]any{
					"schema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"video_context":    map[string]any{"type": "string", "description": "Packed transcript or editing context"},
							"transcript":       map[string]any{"type": "string", "description": "Raw transcript text"},
							"segments":         map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
							"observed_gaps":    map[string]any{"type": "array", "description": "ASR-observed pauses; not cut candidates by themselves", "items": map[string]any{"type": "object"}},
							"candidate_cuts":   map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
							"rendered_cuts":    map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
							"max_decisions":    map[string]any{"type": "integer", "default": 12},
							"default_n_frames": map[string]any{"type": "integer", "default": 15},
							"prompt":           map[string]any{"type": "string"},
						},
					},
				},
			},
		}
	}
	if path == "/api/v1/tools/frame-workflow/narrative-analyze" && method == http.MethodPost {
		operation["summary"] = "Analyze narrative context, characters, relationships, emotion curve, and events"
		operation["requestBody"] = map[string]any{
			"required": true,
			"content": map[string]any{
				"application/json": map[string]any{
					"schema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"video_context":        map[string]any{"type": "string", "description": "Packed transcript or existing context"},
							"transcript":           map[string]any{"type": "string"},
							"segments":             map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
							"decisions":            map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
							"visual_analyses":      map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
							"language":             map[string]any{"type": "string"},
							"prompt":               map[string]any{"type": "string"},
							"workflow_mode":        map[string]any{"type": "string", "enum": []string{"general", "series"}},
							"series_story_context": map[string]any{"type": "string", "description": "User-confirmed external story reference for PC series mode"},
							"series_story_sources": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
							"source_manifest":      map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
						},
					},
				},
			},
		}
	}
	if path == "/api/v1/tools/story-context/versions" && method == http.MethodPost {
		operation["summary"] = "Discover selectable versions for PC series mode"
		operation["requestBody"] = map[string]any{
			"required": true,
			"content": map[string]any{
				"application/json": map[string]any{
					"schema": map[string]any{
						"type":     "object",
						"required": []string{"title"},
						"properties": map[string]any{
							"title":         map[string]any{"type": "string"},
							"season":        map[string]any{"type": "string"},
							"episode_range": map[string]any{"type": "string"},
							"protagonist":   map[string]any{"type": "string"},
							"notes":         map[string]any{"type": "string"},
							"language":      map[string]any{"type": "string", "default": "zh-CN"},
						},
					},
				},
			},
		}
	}
	if path == "/api/v1/tools/story-context/search" && method == http.MethodPost {
		operation["summary"] = "Search editable story and character context for PC series mode"
		operation["requestBody"] = map[string]any{
			"required": true,
			"content": map[string]any{
				"application/json": map[string]any{
					"schema": map[string]any{
						"type":     "object",
						"required": []string{"title"},
						"properties": map[string]any{
							"title":           map[string]any{"type": "string"},
							"season":          map[string]any{"type": "string"},
							"episode_range":   map[string]any{"type": "string"},
							"release_version": map[string]any{"type": "string"},
							"protagonist":     map[string]any{"type": "string"},
							"notes":           map[string]any{"type": "string"},
							"language":        map[string]any{"type": "string", "default": "zh-CN"},
							"force_refresh":   map[string]any{"type": "boolean", "description": "Bypass the cached search result and replace it with fresh provider results"},
							"selected_version": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"id":          map[string]any{"type": "string"},
									"label":       map[string]any{"type": "string"},
									"description": map[string]any{"type": "string"},
									"source_url":  map[string]any{"type": "string"},
								},
							},
						},
					},
				},
			},
		}
		knowledgeProperties := map[string]any{}
		for _, field := range []string{"work_identity", "story_architecture", "episode_scope", "characters", "background", "uncertainties"} {
			knowledgeProperties[field] = map[string]any{"type": "string"}
		}
		operation["responses"] = map[string]any{
			"200": map[string]any{
				"description": "Structured series research; successful sections are preserved when other sections fail",
				"content": jsonContent(map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query":            map[string]any{"type": "string"},
						"knowledge_base":   map[string]any{"type": "object", "properties": knowledgeProperties},
						"warnings":         map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"section": map[string]any{"type": "string"}, "message": map[string]any{"type": "string"}}}},
						"editable_context": map[string]any{"type": "string", "description": "Backward-compatible compiled knowledge text"},
						"sources":          map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"section": map[string]any{"type": "string"}, "title": map[string]any{"type": "string"}, "url": map[string]any{"type": "string"}, "snippet": map[string]any{"type": "string"}}}},
						"searched_at":      map[string]any{"type": "string", "format": "date-time"},
						"cached":           map[string]any{"type": "boolean"},
					},
				}),
			},
			"400": map[string]any{"description": "Bad Request", "content": jsonContent(refSchema("ErrorResponse"))},
			"401": map[string]any{"description": "Unauthorized", "content": jsonContent(refSchema("ErrorResponse"))},
			"502": map[string]any{"description": "No usable research; client should offer manual entry", "content": jsonContent(refSchema("ErrorResponse"))},
		}
	}
	if (path == "/api/v1/tools/frame-workflow/timeline-decisions/review" || path == "/api/v1/tools/frame-workflow/narrative-understanding/review") && method == http.MethodPost {
		resultKey := "decision_result"
		summary := "Review or refine timeline decision output against the task intent"
		if strings.Contains(path, "narrative-understanding") {
			resultKey = "narrative_result"
			summary = "Review or refine narrative understanding output against the task intent"
		}
		operation["summary"] = summary
		operation["requestBody"] = map[string]any{
			"required": true,
			"content": map[string]any{
				"application/json": map[string]any{
					"schema": map[string]any{
						"type":     "object",
						"required": []string{"operation", "intent", resultKey},
						"properties": map[string]any{
							"operation":  map[string]any{"type": "string", "enum": []string{"review", "refine"}},
							"intent":     map[string]any{"type": "object", "description": "Output language, narration style, target duration, narration ratio, and word count"},
							resultKey:    map[string]any{"type": "object"},
							"issues":     map[string]any{"type": "array", "description": "Required when operation is refine", "items": map[string]any{"type": "object"}},
							"project_id": map[string]any{"type": "string"},
						},
					},
				},
			},
		}
	}
	if path == "/api/v1/tools/frame-workflow/subtitles/validate" && method == http.MethodPost {
		operation["summary"] = "Validate and correct subtitles with the OpenAI flash model profile"
		operation["requestBody"] = map[string]any{
			"required": true,
			"content": map[string]any{
				"application/json": map[string]any{
					"schema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"segments":   map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
							"language":   map[string]any{"type": "string"},
							"project_id": map[string]any{"type": "string"},
						},
						"required": []string{"segments"},
					},
				},
			},
		}
	}
	return operation
}

func isEventStreamResponsePath(path string) bool {
	switch path {
	case "/api/v1/tools/generate-script-stream",
		"/api/v1/tools/timeline-view/analyze",
		"/api/v1/tools/timeline-view/decide",
		"/api/v1/tools/frame-workflow/subtitles/validate",
		"/api/v1/tools/frame-workflow/narrative-analyze":
		return true
	case "/api/v1/tools/frame-workflow/timeline-decisions/review",
		"/api/v1/tools/frame-workflow/narrative-understanding/review":
		return true
	default:
		return strings.Contains(path, "stream")
	}
}

func ginPathToOpenAPI(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if strings.HasPrefix(part, ":") {
			parts[i] = "{" + strings.TrimPrefix(part, ":") + "}"
		}
		if strings.HasPrefix(part, "*") {
			parts[i] = "{" + strings.TrimPrefix(part, "*") + "}"
		}
	}
	return strings.Join(parts, "/")
}

func pathParams(path string) []map[string]any {
	var params []map[string]any
	for _, part := range strings.Split(path, "/") {
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			name := strings.TrimSuffix(strings.TrimPrefix(part, "{"), "}")
			schema := map[string]any{"type": "string"}
			if name == "step" {
				schema = refSchema("WorkflowStepName")
			}
			params = append(params, map[string]any{
				"name":     name,
				"in":       "path",
				"required": true,
				"schema":   schema,
			})
		}
	}
	return params
}

func workflowOperationFor(method, path string) map[string]any {
	if !strings.Contains(path, "/projects/{project_id}/workflow") {
		return nil
	}
	operation := map[string]any{
		"summary":     summaryFor(method, path),
		"operationId": operationID(method, path),
		"tags":        []string{"project-workflow"},
		"security":    []map[string]any{{"BearerAuth": []string{}}},
		"parameters":  pathParams(path),
	}
	responses := map[string]any{
		"200": map[string]any{"description": "OK", "content": jsonContent(refSchema("WorkflowStepResponse"))},
		"400": map[string]any{"description": "Bad Request", "content": jsonContent(refSchema("ErrorResponse"))},
		"401": map[string]any{"description": "Unauthorized", "content": jsonContent(refSchema("ErrorResponse"))},
		"404": map[string]any{"description": "Not Found", "content": jsonContent(refSchema("ErrorResponse"))},
		"409": map[string]any{"description": "Workflow conflict", "content": jsonContent(refSchema("WorkflowConflict"))},
		"500": map[string]any{"description": "Internal Server Error", "content": jsonContent(refSchema("ErrorResponse"))},
	}
	if method == http.MethodGet && strings.HasSuffix(path, "/workflow") {
		responses["200"] = map[string]any{"description": "Workflow summary", "content": jsonContent(refSchema("WorkflowSummary"))}
	}
	operation["responses"] = responses
	if method == http.MethodPut {
		operation["requestBody"] = map[string]any{
			"required": true,
			"content":  jsonContent(refSchema("WorkflowStepSaveRequest")),
		}
	}
	return operation
}

func shouldSecure(path string) bool {
	if path == "/api/v1/auth/desktop/requests" || path == "/api/v1/auth/desktop/exchange" || path == "/api/v1/auth/desktop/refresh" {
		return false
	}
	publicPrefixes := []string{"/health", "/api/v1/test", "/api/v1/auth/register", "/api/v1/auth/login", "/api/v1/auth/refresh", "/api/v1/auth/request-email-code", "/api/v1/auth/forgot-password", "/api/v1/auth/reset-password", "/api/v1/auth/oauth", "/api/v1/token-packages/packages", "/api/v1/subscriptions/payments/wechat/webhook", "/api/v1/token-accounts/webhook"}
	for _, prefix := range publicPrefixes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return false
		}
	}
	return strings.HasPrefix(path, "/api/")
}

func tagFor(path string) string {
	trimmed := strings.TrimPrefix(path, "/api/v1/")
	if strings.HasPrefix(path, "/health") {
		return "health"
	}
	if trimmed == path || trimmed == "" {
		return "default"
	}
	parts := strings.Split(trimmed, "/")
	if len(parts) >= 2 && parts[0] == "admin" {
		return "admin-" + parts[1]
	}
	return parts[0]
}

func summaryFor(method, path string) string {
	return method + " " + path
}

func operationID(method, path string) string {
	id := strings.ToLower(method) + "_" + strings.Trim(path, "/")
	replacer := strings.NewReplacer("/", "_", "{", "", "}", "", "-", "_", "*", "")
	return replacer.Replace(id)
}

func jsonContent(schema map[string]any) map[string]any {
	return map[string]any{"application/json": map[string]any{"schema": schema}}
}

func refSchema(name string) map[string]any {
	return map[string]any{"$ref": "#/components/schemas/" + name}
}

func openAPISchemas() map[string]any {
	return map[string]any{
		"GenericResponse": map[string]any{"type": "object", "additionalProperties": true},
		"ErrorResponse": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"error":   map[string]any{"type": "string"},
				"message": map[string]any{"type": "string"},
				"success": map[string]any{"type": "boolean"},
			},
		},
		"AuthTokens": map[string]any{
			"type":     "object",
			"required": []string{"access_token", "refresh_token", "token_type", "expires_in"},
			"properties": map[string]any{
				"access_token":  map[string]any{"type": "string"},
				"refresh_token": map[string]any{"type": "string"},
				"token_type":    map[string]any{"type": "string", "example": "bearer"},
				"expires_in":    map[string]any{"type": "integer"},
			},
		},
		"UserProfile": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"user_id":     map[string]any{"type": "string", "format": "uuid"},
				"username":    map[string]any{"type": "string"},
				"email":       map[string]any{"type": "string", "format": "email"},
				"is_active":   map[string]any{"type": "boolean"},
				"is_verified": map[string]any{"type": "boolean"},
				"is_admin":    map[string]any{"type": "boolean"},
			},
		},
		"Project": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":          map[string]any{"type": "string", "format": "uuid"},
				"name":        map[string]any{"type": "string"},
				"description": map[string]any{"type": "string", "nullable": true},
				"status":      map[string]any{"type": "string"},
			},
		},
		"WorkflowStepName": map[string]any{
			"type": "string",
			"enum": []string{"video-source", "subtitles", "config", "script", "voiceover"},
		},
		"WorkflowSummary": map[string]any{
			"type":     "object",
			"required": []string{"project_id", "workflow_revision", "video_source", "subtitles", "config", "script", "voiceover"},
			"properties": map[string]any{
				"project_id":        map[string]any{"type": "string", "format": "uuid"},
				"workflow_revision": map[string]any{"type": "integer", "minimum": 1},
				"video_source":      refSchema("WorkflowStepPayload"),
				"subtitles":         refSchema("WorkflowStepPayload"),
				"config":            refSchema("WorkflowStepPayload"),
				"script":            refSchema("WorkflowStepPayload"),
				"voiceover":         refSchema("WorkflowStepPayload"),
				"workflow_root":     refSchema("WorkflowRoot"),
				"updated_at":        map[string]any{"type": "string", "format": "date-time"},
			},
		},
		"WorkflowStepPayload": map[string]any{
			"type":                 "object",
			"additionalProperties": true,
			"properties": map[string]any{
				"step_id":           map[string]any{"type": "string", "format": "uuid"},
				"content_hash":      map[string]any{"type": "string"},
				"dependency_hash":   map[string]any{"type": "string"},
				"input_hash":        map[string]any{"type": "string"},
				"payload_hash":      map[string]any{"type": "string"},
				"workflow_revision": map[string]any{"type": "integer", "minimum": 1},
			},
		},
		"WorkflowRoot": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"workflow_id":    map[string]any{"type": "string", "format": "uuid"},
				"project_id":     map[string]any{"type": "string", "format": "uuid"},
				"current_step":   map[string]any{"type": "string", "nullable": true},
				"revision":       map[string]any{"type": "integer", "minimum": 1},
				"status":         map[string]any{"type": "string", "enum": []string{"active", "failed", "completed", "paused"}},
				"failure_reason": map[string]any{"type": "string", "nullable": true},
				"last_step_type": map[string]any{"type": "string", "nullable": true},
				"last_step_id":   map[string]any{"type": "string", "format": "uuid", "nullable": true},
				"updated_at":     map[string]any{"type": "string", "format": "date-time"},
			},
		},
		"WorkflowStepSaveRequest": map[string]any{
			"type":     "object",
			"required": []string{"workflow_revision"},
			"properties": map[string]any{
				"workflow_revision": map[string]any{"type": "integer", "minimum": 1},
				"dependency_hash":   map[string]any{"type": "string", "nullable": true},
				"status":            map[string]any{"type": "string", "default": "active"},
				"data":              map[string]any{"type": "object", "additionalProperties": true},
			},
			"additionalProperties": true,
		},
		"WorkflowStepResponse": map[string]any{
			"type":     "object",
			"required": []string{"project_id", "step", "workflow_revision"},
			"properties": map[string]any{
				"project_id":        map[string]any{"type": "string", "format": "uuid"},
				"step":              refSchema("WorkflowStepName"),
				"workflow_revision": map[string]any{"type": "integer", "minimum": 1},
				"step_id":           map[string]any{"type": "string", "format": "uuid"},
				"content_hash":      map[string]any{"type": "string"},
				"dependency_hash":   map[string]any{"type": "string"},
				"input_hash":        map[string]any{"type": "string"},
				"payload_hash":      map[string]any{"type": "string"},
				"idempotent":        map[string]any{"type": "boolean"},
				"video_source":      refSchema("WorkflowStepPayload"),
				"subtitles":         refSchema("WorkflowStepPayload"),
				"config":            refSchema("WorkflowStepPayload"),
				"script":            refSchema("WorkflowStepPayload"),
				"voiceover":         refSchema("WorkflowStepPayload"),
			},
		},
		"WorkflowConflict": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"error":   map[string]any{"type": "string", "example": "conflict"},
				"message": map[string]any{"type": "string"},
				"detail":  map[string]any{"type": "object", "additionalProperties": true},
				"success": map[string]any{"type": "boolean", "example": false},
			},
		},
		"TokenBalance": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"user_id":          map[string]any{"type": "string", "format": "uuid"},
				"total_tokens":     map[string]any{"type": "integer"},
				"used_tokens":      map[string]any{"type": "integer"},
				"remaining_tokens": map[string]any{"type": "integer"},
			},
		},
	}
}

func swaggerUIHTML(openAPIURL string) string {
	return `<!doctype html><html><head><title>NarratoAI API Docs</title><link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css"></head><body><div id="swagger-ui"></div><script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script><script>SwaggerUIBundle({url:"` + openAPIURL + `",dom_id:"#swagger-ui"});</script></body></html>`
}

func redocHTML(openAPIURL string) string {
	return `<!doctype html><html><head><title>NarratoAI API Redoc</title></head><body><redoc spec-url="` + openAPIURL + `"></redoc><script src="https://cdn.jsdelivr.net/npm/redoc@next/bundles/redoc.standalone.js"></script></body></html>`
}
