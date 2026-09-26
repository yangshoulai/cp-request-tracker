package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = host;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) {
		return 1;
	}
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {
		stored_host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

const (
	pluginID     = "cpa-request-tracker"
	resourcePath = "/page"
)

var pluginVersion = "0.1.6"

//go:embed web/index.html
var indexHTML string

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

type registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      pluginapi.Metadata       `json:"metadata"`
	Capabilities  registrationCapabilities `json:"capabilities"`
}

type registrationCapabilities struct {
	UsagePlugin   bool `json:"usage_plugin"`
	ManagementAPI bool `json:"management_api"`
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type managementRequest struct {
	Method         string      `json:"Method"`
	Path           string      `json:"Path"`
	Headers        http.Header `json:"Headers"`
	Query          url.Values  `json:"Query"`
	Body           []byte      `json:"Body"`
	HostCallbackID string      `json:"host_callback_id,omitempty"`
}

type managementRegistration struct {
	Resources []managementResource `json:"resources,omitempty"`
	Routes    []managementRoute    `json:"routes,omitempty"`
}

type managementResource struct {
	Path        string `json:"Path"`
	Menu        string `json:"Menu"`
	Description string `json:"Description"`
}

type managementRoute struct {
	Method      string `json:"Method"`
	Path        string `json:"Path"`
	Description string `json:"Description,omitempty"`
}

type managementResponse struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers"`
	Body       []byte      `json:"Body"`
}

type pluginConfig struct {
	RetentionDays int `yaml:"retention_days"`
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	C.store_host_api(host)
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required", http.StatusBadRequest))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := handleMethod(C.GoString(method), requestBytes)
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error(), http.StatusInternalServerError))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, length C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
	_ = length
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	closeStore()
}

func handleMethod(method string, raw []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if errConfigure := configure(raw); errConfigure != nil {
			return nil, errConfigure
		}
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodUsageHandle:
		var record pluginapi.UsageRecord
		if errUnmarshal := json.Unmarshal(raw, &record); errUnmarshal != nil {
			return nil, fmt.Errorf("decode usage record: %w", errUnmarshal)
		}
		if errSave := saveCall(record); errSave != nil {
			return nil, errSave
		}
		return okEnvelope(struct{}{})
	case pluginabi.MethodManagementRegister:
		return okEnvelope(managementRegistration{
			Resources: []managementResource{{
				Path:        resourcePath,
				Menu:        "调用记录",
				Description: "按账号和模型查看调用元数据，并下载 CPA 请求日志。",
			}},
			Routes: []managementRoute{
				{Method: http.MethodGet, Path: "/plugins/" + pluginID + "/records", Description: "分页查询请求记录"},
			},
		})
	case pluginabi.MethodManagementHandle:
		return handleManagement(raw)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method, http.StatusBadRequest), nil
	}
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             "CPA Request Tracker",
			Version:          pluginVersion,
			Author:           "CPA Request Tracker contributors",
			GitHubRepository: "https://github.com/yangshoulai/cp-request-tracker",
			ConfigFields: []pluginapi.ConfigField{{
				Name:        "retention_days",
				Type:        pluginapi.ConfigFieldTypeInteger,
				Description: "请求元数据最多保留的天数，默认 7 天；允许 1 到 3650 天。",
			}},
		},
		Capabilities: registrationCapabilities{UsagePlugin: true, ManagementAPI: true},
	}
}

func configure(raw []byte) error {
	var request lifecycleRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &request); errUnmarshal != nil {
			return fmt.Errorf("decode plugin configuration request: %w", errUnmarshal)
		}
	}
	config := pluginConfig{RetentionDays: 7}
	if len(request.ConfigYAML) > 0 {
		if errUnmarshal := yaml.Unmarshal(request.ConfigYAML, &config); errUnmarshal != nil {
			return fmt.Errorf("decode plugin configuration: %w", errUnmarshal)
		}
	}
	if config.RetentionDays == 0 {
		config.RetentionDays = 7
	}
	if config.RetentionDays < 1 || config.RetentionDays > 3650 {
		return fmt.Errorf("retention_days must be between 1 and 3650")
	}
	setRetentionDays(config.RetentionDays)
	return nil
}

func handleManagement(raw []byte) ([]byte, error) {
	var request managementRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &request); errUnmarshal != nil {
			return nil, fmt.Errorf("decode management request: %w", errUnmarshal)
		}
	}
	if strings.HasSuffix(request.Path, resourcePath) && request.Method == http.MethodGet {
		return okEnvelope(htmlResponse(http.StatusOK, []byte(indexHTML)))
	}
	if strings.HasSuffix(request.Path, "/records") && request.Method == http.MethodGet {
		return handleListRecords(request)
	}
	return okEnvelope(jsonResponse(http.StatusNotFound, map[string]string{"error": "not found"}))
}

func htmlResponse(status int, body []byte) managementResponse {
	return managementResponse{StatusCode: status, Headers: http.Header{"content-type": []string{"text/html; charset=utf-8"}, "cache-control": []string{"no-store"}}, Body: body}
}

func jsonResponse(status int, value any) managementResponse {
	body, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		status = http.StatusInternalServerError
		body = []byte(`{"error":"failed to encode response"}`)
	}
	return managementResponse{StatusCode: status, Headers: http.Header{"content-type": []string{"application/json; charset=utf-8"}, "cache-control": []string{"no-store"}}, Body: body}
}

func okEnvelope(value any) ([]byte, error) {
	result, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(envelope{OK: true, Result: result})
}

func errorEnvelope(code, message string, status int) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message, HTTPStatus: status}})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}

func callHost(method string, payload any, callbackID string) (json.RawMessage, error) {
	request, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return nil, fmt.Errorf("encode host callback request: %w", errMarshal)
	}
	// Host auth callbacks receive the callback id in the same JSON object as their arguments.
	if object, ok := payload.(map[string]any); ok {
		object["host_callback_id"] = callbackID
		request, errMarshal = json.Marshal(object)
		if errMarshal != nil {
			return nil, fmt.Errorf("encode host callback request: %w", errMarshal)
		}
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var response C.cliproxy_buffer
	var requestPtr *C.uint8_t
	if len(request) > 0 {
		buffer := C.CBytes(request)
		if buffer == nil {
			return nil, fmt.Errorf("allocate host callback request")
		}
		defer C.free(buffer)
		requestPtr = (*C.uint8_t)(buffer)
	}
	status := C.call_host_api(cMethod, requestPtr, C.size_t(len(request)), &response)
	var responseBytes []byte
	if response.ptr != nil && response.len > 0 {
		responseBytes = C.GoBytes(response.ptr, C.int(response.len))
	}
	if response.ptr != nil {
		C.free_host_buffer(response.ptr, response.len)
	}
	if status != 0 || len(responseBytes) == 0 {
		return nil, fmt.Errorf("host callback %s failed", method)
	}
	var result envelope
	if errUnmarshal := json.Unmarshal(responseBytes, &result); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host callback response: %w", errUnmarshal)
	}
	if !result.OK {
		if result.Error != nil {
			return nil, fmt.Errorf("%s: %s", result.Error.Code, result.Error.Message)
		}
		return nil, fmt.Errorf("host callback %s failed", method)
	}
	return result.Result, nil
}
