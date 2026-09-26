package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type authListResponse struct {
	Files []pluginapi.HostAuthFileEntry `json:"files"`
}

func handleListRecords(request managementRequest) ([]byte, error) {
	filter := callFilter{
		Page:            parsePositiveInt(request.Query.Get("page"), 1),
		PageSize:        parsePositiveInt(request.Query.Get("page_size"), 25),
		AccountType:     request.Query.Get("account_type"),
		Email:           request.Query.Get("email"),
		Model:           request.Query.Get("model"),
		ReasoningEffort: request.Query.Get("reasoning_effort"),
		Keyword:         request.Query.Get("q"),
	}
	result, errAuthList := callHost(pluginabi.MethodHostAuthList, map[string]any{}, request.HostCallbackID)
	if errAuthList != nil {
		return okEnvelope(jsonResponse(http.StatusBadGateway, map[string]string{"error": "CPA 账号列表读取失败：" + errAuthList.Error()}))
	}
	var accounts authListResponse
	if errUnmarshal := json.Unmarshal(result, &accounts); errUnmarshal != nil {
		return nil, fmt.Errorf("decode CPA account list: %w", errUnmarshal)
	}
	page, errList := listCalls(nil, filter, accounts.Files)
	if errList != nil {
		return okEnvelope(jsonResponse(http.StatusInternalServerError, map[string]string{"error": errList.Error()}))
	}
	return okEnvelope(jsonResponse(http.StatusOK, page))
}

func parsePositiveInt(raw string, fallback int) int {
	value, errParse := strconv.Atoi(strings.TrimSpace(raw))
	if errParse != nil || value < 1 {
		return fallback
	}
	return value
}
