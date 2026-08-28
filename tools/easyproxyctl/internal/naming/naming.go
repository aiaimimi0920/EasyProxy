package naming

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"

	"github.com/aiaimimi0920/EasyProxy/tools/easyproxyctl/internal/topology"
)

const maxResourceNameLength = 63

var invalidNameCharacters = regexp.MustCompile(`[^a-z0-9-]+`)

type ResourceNames struct {
	PagesProject string `json:"pages_project"`
	D1Database   string `json:"d1_database"`
	ECHWorker    string `json:"ech_worker"`
	R2Bucket     string `json:"r2_bucket"`
}

func Resolve(value *topology.Topology) ResourceNames {
	configured := value.Cloudflare.Resources
	return ResourceNames{
		PagesProject: resolve(configured.PagesProject, value.DeploymentName+"-misub-pages"),
		D1Database:   resolve(configured.D1Database, value.DeploymentName+"-misub-d1"),
		ECHWorker:    resolve(configured.ECHWorker, value.DeploymentName+"-ech-worker"),
		R2Bucket:     resolve(configured.R2Bucket, value.DeploymentName+"-artifacts"),
	}
}

func Normalize(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = invalidNameCharacters.ReplaceAllString(normalized, "-")
	normalized = strings.Trim(normalized, "-")
	if len(normalized) <= maxResourceNameLength {
		return normalized
	}
	sum := sha256.Sum256([]byte(normalized))
	suffix := fmt.Sprintf("-%x", sum[:5])
	prefix := strings.TrimRight(normalized[:maxResourceNameLength-len(suffix)], "-")
	return prefix + suffix
}

func resolve(explicit, fallback string) string {
	if explicit != "" {
		return explicit
	}
	return Normalize(fallback)
}
