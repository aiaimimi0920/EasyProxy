package monitor

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"easy_proxies/internal/profile"
	"easy_proxies/internal/store"
)

func parseDeviceResourcePath(r *http.Request) (string, string, error) {
	const prefix = "/api/local-server/devices/"
	escaped := strings.TrimPrefix(r.URL.EscapedPath(), prefix)
	parts := strings.Split(strings.Trim(escaped, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return "", "", errLocalServerNotFound
	}
	rawID, err := url.PathUnescape(parts[0])
	if err != nil {
		return "", "", fmt.Errorf("%w: invalid escaped device_id", errLocalServerValidation)
	}
	deviceID, err := profile.NormalizeDeviceID(rawID)
	if err != nil {
		return "", "", fmt.Errorf("%w: %v", errLocalServerValidation, err)
	}
	action := strings.Join(parts[1:], "/")
	return deviceID, action, nil
}

func parseMappingResourcePath(r *http.Request) (string, error) {
	const prefix = "/api/local-server/ip-mappings/"
	escaped := strings.Trim(strings.TrimPrefix(r.URL.EscapedPath(), prefix), "/")
	if escaped == "" || strings.Contains(escaped, "/") {
		return "", errLocalServerNotFound
	}
	mappingID, err := url.PathUnescape(escaped)
	if err != nil || strings.TrimSpace(mappingID) == "" {
		return "", fmt.Errorf("%w: invalid mapping_id", errLocalServerValidation)
	}
	return strings.TrimSpace(mappingID), nil
}

func decodeJSONBody(r *http.Request, target any) error {
	if r == nil || r.Body == nil {
		return fmt.Errorf("%w: request body is required", errLocalServerValidation)
	}
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		return fmt.Errorf("%w: invalid request body: %v", errLocalServerValidation, err)
	}
	return nil
}

func expectedRevision(r *http.Request, bodyRevision *int64) (int64, error) {
	if bodyRevision != nil && *bodyRevision < 0 {
		return 0, fmt.Errorf("%w: expected_revision must be non-negative", errLocalServerValidation)
	}
	ifMatch := strings.TrimSpace(r.Header.Get("If-Match"))
	ifNoneMatch := strings.TrimSpace(r.Header.Get("If-None-Match"))
	if ifMatch != "" && ifNoneMatch != "" {
		return 0, fmt.Errorf("%w: If-Match and If-None-Match are mutually exclusive", errLocalServerValidation)
	}
	headerSet := false
	headerRevision := int64(0)
	switch {
	case ifNoneMatch != "":
		if ifNoneMatch != "*" {
			return 0, fmt.Errorf("%w: If-None-Match must be *", errLocalServerValidation)
		}
		headerSet = true
	case ifMatch != "":
		trimmed := strings.Trim(ifMatch, `"`)
		parsed, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil || parsed < 0 {
			return 0, fmt.Errorf("%w: invalid If-Match revision", errLocalServerValidation)
		}
		headerSet = true
		headerRevision = parsed
	}
	if headerSet {
		if bodyRevision != nil && *bodyRevision != headerRevision {
			return 0, fmt.Errorf("%w: header and body revisions contradict", errLocalServerValidation)
		}
		return headerRevision, nil
	}
	if bodyRevision != nil {
		return *bodyRevision, nil
	}
	return 0, nil
}

func expectedRevisionFromOptionalBody(r *http.Request) (int64, error) {
	var bodyRevision *int64
	if r != nil && r.Body != nil && r.ContentLength != 0 {
		var body expectedRevisionRequest
		if err := decodeJSONBody(r, &body); err != nil {
			return 0, err
		}
		bodyRevision = body.ExpectedRevision
	}
	return expectedRevision(r, bodyRevision)
}

func normalizeMappingCIDR(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if prefix, err := netip.ParsePrefix(trimmed); err == nil {
		return prefix.Masked().String(), nil
	}
	addr, err := netip.ParseAddr(trimmed)
	if err != nil {
		return "", fmt.Errorf("%w: invalid IP or CIDR %q", errLocalServerValidation, value)
	}
	bits := 128
	if addr.Is4() {
		bits = 32
	}
	return netip.PrefixFrom(addr, bits).String(), nil
}

func newMappingID() (string, error) {
	bytes := make([]byte, 12)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate mapping id: %w", err)
	}
	return "map-" + hex.EncodeToString(bytes), nil
}

func classifyLocalServerValidation(err error) error {
	if err == nil {
		return nil
	}
	var conflict *store.RevisionConflictError
	if errors.As(err, &conflict) || errors.Is(err, store.ErrDeviceNotFound) || errors.Is(err, errLocalServerNotFound) {
		return err
	}
	if errors.Is(err, profile.ErrInvalidDefinition) || errors.Is(err, profile.ErrInvalidDeviceID) {
		return fmt.Errorf("%w: %v", errLocalServerValidation, err)
	}
	return err
}

func (s *Server) writeLocalServerError(w http.ResponseWriter, err error) {
	response := apiError{Error: "internal_error"}
	status := http.StatusInternalServerError
	var conflict *store.RevisionConflictError
	switch {
	case errors.Is(err, errReloadInProgress):
		status = http.StatusConflict
		response.Error = "reload_in_progress"
		response.NeedReload = true
	case errors.As(err, &conflict):
		status = http.StatusConflict
		response.Error = "revision_conflict"
		response.CurrentRevision = conflict.CurrentRevision
	case errors.Is(err, errLocalServerNotFound), errors.Is(err, store.ErrDeviceNotFound):
		status = http.StatusNotFound
		response.Error = "not_found"
	case errors.Is(err, errLocalServerValidation):
		status = http.StatusUnprocessableEntity
		response.Error = err.Error()
	default:
		response.Error = err.Error()
	}
	w.WriteHeader(status)
	writeJSON(w, response)
}
