package monitor

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// acceptsNodesGzip respects explicit gzip rejection, including a wildcard fallback.
func acceptsNodesGzip(header string) bool {
	wildcard := false
	for _, item := range strings.Split(header, ",") {
		parts := strings.Split(item, ";")
		coding := strings.ToLower(strings.TrimSpace(parts[0]))
		quality := 1.0
		for _, parameter := range parts[1:] {
			key, value, ok := strings.Cut(parameter, "=")
			if ok && strings.EqualFold(strings.TrimSpace(key), "q") {
				parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
				if err != nil || parsed < 0 || parsed > 1 {
					quality = 0
				} else {
					quality = parsed
				}
			}
		}
		if coding == "gzip" {
			return quality > 0
		}
		if coding == "*" {
			wildcard = quality > 0
		}
	}
	return wildcard
}

// writeNodesJSON preserves the full node contract while reducing negotiated wire size.
func writeNodesJSON(w http.ResponseWriter, r *http.Request, payload any) {
	w.Header().Add("Vary", "Accept-Encoding")
	if !acceptsNodesGzip(strings.Join(r.Header.Values("Accept-Encoding"), ",")) {
		writeJSON(w, payload)
		return
	}

	var plain bytes.Buffer
	encoder := json.NewEncoder(&plain)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(payload); err != nil {
		http.Error(w, "failed to encode nodes response", http.StatusInternalServerError)
		return
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(plain.Bytes()); err != nil {
		http.Error(w, "failed to compress nodes response", http.StatusInternalServerError)
		return
	}
	if err := writer.Close(); err != nil {
		http.Error(w, "failed to compress nodes response", http.StatusInternalServerError)
		return
	}
	if compressed.Len() >= plain.Len() {
		writeJSON(w, payload)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Set("Content-Length", strconv.Itoa(compressed.Len()))
	_, _ = w.Write(compressed.Bytes())
}
